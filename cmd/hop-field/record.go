package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// sample is one line of rec.jsonl: what this machine looked like at T.
type sample struct {
	T       string    `json:"t"`
	Host    string    `json:"host"`
	GapSec  int       `json:"gapSec,omitempty"` // since the last sample when over 3 min: asleep or off
	LocalIP string    `json:"localIp,omitempty"`
	Agent   agentInfo `json:"agent"`
	Nets    []netInfo `json:"nets"`
	Log     []string  `json:"log,omitempty"` // new lines of the agent's log that matter
	LogMore int       `json:"logMore,omitempty"`
	Err     string    `json:"err,omitempty"`
}

type agentInfo struct {
	Version string `json:"version,omitempty"`
	Pid     int    `json:"pid,omitempty"` // a new number is a restart
	Up      bool   `json:"up"`            // the local API answered
}

type netInfo struct {
	Name        string     `json:"name"`
	MeshIP      string     `json:"meshIp"`
	CertLeftSec int        `json:"certLeftSec"`
	CertEnds    string     `json:"certEnds"`
	Connected   bool       `json:"connected"`
	Peers       []peerInfo `json:"peers"`
}

type peerInfo struct {
	IP     string  `json:"ip"`
	Name   string  `json:"name,omitempty"`
	Direct bool    `json:"direct"`
	Remote string  `json:"remote,omitempty"`
	Sent   int     `json:"pingSent"`
	Got    int     `json:"pingGot"`
	AvgMs  float64 `json:"pingAvgMs,omitempty"`
}

// logMatters picks the lines of the agent's log worth keeping: every
// renewal, every failure, every restart of a network and every watchdog.
var logMatters = regexp.MustCompile(`level=(error|warning|fatal)|CRITICAL|WATCHDOG|panic|\[renew [^\]]*\] (cert past|renewal failed|retry|certificate renewed|cert needs|cert still|Nebula|failed|instance|could not|no embedded)|\[recovery|401|certificate is expired|network change detected|sleep/wake detected|Tunnel status|listening on`)

const maxLogLines = 40

func runRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "directory for rec.jsonl")
	apiDir := fs.String("api-dir", "", "directory with system-local-api-port and -token (default: the desktop app's)")
	logPath := fs.String("log", "/var/log/hop-agent.log", "the agent's log")
	fs.Parse(args)
	if *apiDir == "" {
		home, _ := os.UserHomeDir()
		*apiDir = filepath.Join(home, "Library", "Application Support", "hopssh")
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}

	now := time.Now()
	s := sample{T: now.Format(time.RFC3339)}
	s.Host, _ = os.Hostname()
	s.LocalIP = localIP()
	s.Agent.Pid = agentPid()

	// The time of the last sample tells whether the machine was away.
	stamp := filepath.Join(*dir, "rec.last")
	if b, err := os.ReadFile(stamp); err == nil {
		if last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil {
			if gap := int(now.Sub(last).Seconds()); gap > 180 {
				s.GapSec = gap
			}
		}
	}
	os.WriteFile(stamp, []byte(s.T), 0o600)

	if err := s.readAgent(*apiDir); err != nil {
		s.Err = err.Error()
	}
	s.Log, s.LogMore = newLogLines(*logPath, filepath.Join(*dir, "rec.logpos"))

	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(*dir, "rec.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// readAgent asks the agent's local API for its networks and peers, and
// pings every peer that is not the lighthouse.
func (s *sample) readAgent(apiDir string) error {
	port, err := os.ReadFile(filepath.Join(apiDir, "system-local-api-port"))
	if err != nil {
		return fmt.Errorf("local API port: %w", err)
	}
	token, err := os.ReadFile(filepath.Join(apiDir, "system-local-api-token"))
	if err != nil {
		return fmt.Errorf("local API token: %w", err)
	}
	base := "http://127.0.0.1:" + strings.TrimSpace(string(port))
	get := func(path string, v any) error {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("%s: %s", path, resp.Status)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
	}

	var st struct {
		Version     string `json:"version"`
		Enrollments []struct {
			Name         string `json:"name"`
			MeshIP       string `json:"meshIp"`
			CertNotAfter string `json:"certNotAfter"`
			Connected    bool   `json:"connected"`
		} `json:"enrollments"`
	}
	if err := get("/local/status", &st); err != nil {
		return err
	}
	s.Agent.Up = true
	s.Agent.Version = st.Version
	for _, e := range st.Enrollments {
		n := netInfo{Name: e.Name, MeshIP: e.MeshIP, CertEnds: e.CertNotAfter, Connected: e.Connected, Peers: []peerInfo{}}
		if end, err := time.Parse(time.RFC3339, e.CertNotAfter); err == nil {
			n.CertLeftSec = int(time.Until(end).Seconds())
		}
		var ps struct {
			Peers []struct {
				VpnAddr      string `json:"vpnAddr"`
				Direct       bool   `json:"direct"`
				RemoteAddr   string `json:"remoteAddr"`
				Name         string `json:"name"`
				IsLighthouse bool   `json:"isLighthouse"`
			} `json:"peers"`
		}
		if err := get("/local/peers?enrollment="+e.Name, &ps); err == nil {
			for _, p := range ps.Peers {
				if p.IsLighthouse {
					continue
				}
				pi := peerInfo{IP: p.VpnAddr, Name: p.Name, Direct: p.Direct, Remote: p.RemoteAddr}
				pi.Sent, pi.Got, pi.AvgMs = ping(p.VpnAddr)
				n.Peers = append(n.Peers, pi)
			}
		}
		s.Nets = append(s.Nets, n)
	}
	return nil
}

var (
	pingCount = regexp.MustCompile(`(\d+) packets transmitted, (\d+) (?:packets )?received`)
	pingAvg   = regexp.MustCompile(`= [\d.]+/([\d.]+)/`)
)

// ping sends five pings within about two seconds.
func ping(ip string) (sent, got int, avgMs float64) {
	if net.ParseIP(ip) == nil {
		return 0, 0, 0
	}
	out, _ := exec.Command("ping", "-c", "5", "-i", "0.3", "-W", "1000", "-t", "4", ip).CombinedOutput()
	if m := pingCount.FindSubmatch(out); m != nil {
		sent, _ = strconv.Atoi(string(m[1]))
		got, _ = strconv.Atoi(string(m[2]))
	}
	if m := pingAvg.FindSubmatch(out); m != nil {
		avgMs, _ = strconv.ParseFloat(string(m[1]), 64)
	}
	return sent, got, avgMs
}

// localIP is the address this machine uses towards the internet: it
// changes when the machine changes its network. Nothing is sent.
func localIP() string {
	c, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

func agentPid() int {
	out, err := exec.Command("pgrep", "-f", "hop-agent serve").Output()
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]))
	return pid
}

// newLogLines returns the lines that matter among those added to the log
// since the last call. The first call only notes where the log ends.
func newLogLines(path, posFile string) (lines []string, more int) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0
	}
	pos := int64(-1)
	if b, err := os.ReadFile(posFile); err == nil {
		pos, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	}
	defer func() { os.WriteFile(posFile, []byte(strconv.FormatInt(fi.Size(), 10)), 0o600) }()
	if pos < 0 {
		return nil, 0
	}
	if pos > fi.Size() { // the log was rotated
		pos = 0
	}
	if _, err := f.Seek(pos, io.SeekStart); err != nil {
		return nil, 0
	}
	sc := bufio.NewScanner(io.LimitReader(f, fi.Size()-pos))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !logMatters.MatchString(line) || strings.Contains(line, "recv_error config") {
			continue
		}
		if len(lines) >= maxLogLines {
			more++
			continue
		}
		if len(line) > 300 {
			line = line[:300]
		}
		lines = append(lines, line)
	}
	return lines, more
}
