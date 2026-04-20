package portmap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// canonicalDeviceXML is a minimal but spec-shaped IGD device
// description that exposes a WANIPConnection:1 service. Used by the
// mock device-description HTTP server in unit tests.
func canonicalDeviceXML(controlURL string) string {
	return `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
    <friendlyName>Mock IGD</friendlyName>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
        <deviceList>
          <device>
            <deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
            <serviceList>
              <service>
                <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
                <serviceId>urn:upnp-org:serviceId:WANIPConn1</serviceId>
                <controlURL>` + controlURL + `</controlURL>
                <eventSubURL>/evt</eventSubURL>
                <SCPDURL>/scpd.xml</SCPDURL>
              </service>
            </serviceList>
          </device>
        </deviceList>
      </device>
    </deviceList>
  </device>
</root>`
}

func TestParseSSDPReply(t *testing.T) {
	raw := []byte("HTTP/1.1 200 OK\r\n" +
		"CACHE-CONTROL: max-age=120\r\n" +
		"LOCATION: http://192.168.1.1/desc.xml\r\n" +
		"ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n" +
		"SERVER: TP-LINK/TP-LINK UPnP/1.1\r\n\r\n")
	r, ok := parseSSDPReply(raw)
	if !ok {
		t.Fatal("parseSSDPReply: expected ok=true")
	}
	if r.Location != "http://192.168.1.1/desc.xml" {
		t.Errorf("Location: got %q", r.Location)
	}
	if !strings.Contains(r.ST, "InternetGatewayDevice") {
		t.Errorf("ST: got %q", r.ST)
	}
	if r.Server == "" {
		t.Errorf("Server header missing")
	}
}

func TestParseSSDPReply_Malformed(t *testing.T) {
	for _, in := range []string{
		"",
		"not http",
		"HTTP/1.1 200 OK\r\nlocation\r\n", // header without colon
	} {
		_, ok := parseSSDPReply([]byte(in))
		if ok {
			t.Errorf("expected ok=false for %q", in)
		}
	}
}

func TestIsIGDST(t *testing.T) {
	for _, st := range []string{
		"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
		"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
		"urn:schemas-upnp-org:service:WANIPConnection:1",
		"urn:schemas-upnp-org:service:WANPPPConnection:1",
	} {
		if !isIGDST(st) {
			t.Errorf("expected IGD ST: %q", st)
		}
	}
	for _, st := range []string{
		"",
		"urn:schemas-upnp-org:device:MediaServer:1",
		"upnp:rootdevice",
	} {
		if isIGDST(st) {
			t.Errorf("expected non-IGD ST: %q", st)
		}
	}
}

func TestFetchService_PicksWANIPConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(canonicalDeviceXML("/ctl/IPConn")))
	}))
	defer srv.Close()

	svc, err := fetchService(ctx, srv.URL+"/desc.xml")
	if err != nil {
		t.Fatalf("fetchService: %v", err)
	}
	if !strings.HasSuffix(svc.ControlURL, "/ctl/IPConn") {
		t.Errorf("ControlURL = %q, want suffix /ctl/IPConn", svc.ControlURL)
	}
	if !strings.Contains(svc.ServiceType, "WANIPConnection:1") {
		t.Errorf("ServiceType = %q", svc.ServiceType)
	}
}

func TestFetchService_NoServiceReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><root><device><deviceType>media</deviceType></device></root>`))
	}))
	defer srv.Close()

	if _, err := fetchService(ctx, srv.URL+"/desc.xml"); err != errNoService {
		t.Errorf("expected errNoService, got %v", err)
	}
}

func TestExtractErrorCode(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<s:Envelope><s:Body><s:Fault><faultcode>s:Client</faultcode>
<detail><UPnPError><errorCode>725</errorCode>
<errorDescription>OnlyPermanentLeasesSupported</errorDescription></UPnPError></detail>
</s:Fault></s:Body></s:Envelope>`)
	if got := extractErrorCode(body); got != 725 {
		t.Errorf("extractErrorCode: got %d want 725", got)
	}
	if got := extractErrorCode([]byte("no fault")); got != 0 {
		t.Errorf("expected 0 on no error, got %d", got)
	}
}

func TestParseGetExternalIP(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
      <NewExternalIPAddress>203.0.113.5</NewExternalIPAddress>
    </u:GetExternalIPAddressResponse>
  </s:Body>
</s:Envelope>`)
	ip, err := parseGetExternalIP(body)
	if err != nil {
		t.Fatalf("parseGetExternalIP: %v", err)
	}
	if ip != "203.0.113.5" {
		t.Errorf("got %q, want 203.0.113.5", ip)
	}
}

func TestParseGetExternalIP_Empty(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><s:Envelope><s:Body><u:GetExternalIPAddressResponse><NewExternalIPAddress></NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`)
	if _, err := parseGetExternalIP(body); err == nil {
		t.Error("expected error on empty IP")
	}
}

// TestUPnP_Map_Hermetic exercises the SOAP path end-to-end against an
// httptest server that pretends to be both the device-description host
// AND the SOAP control URL. SSDP discovery is bypassed by injecting
// the cached service directly.
func TestUPnP_Map_Hermetic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ctl/IPConn", func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPAction")
		if strings.Contains(action, "AddPortMapping") {
			w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/>
  </s:Body>
</s:Envelope>`))
			return
		}
		if strings.Contains(action, "GetExternalIPAddress") {
			w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
      <NewExternalIPAddress>203.0.113.5</NewExternalIPAddress>
    </u:GetExternalIPAddressResponse>
  </s:Body>
</s:Envelope>`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &UPnP{
		service: &upnpService{
			ControlURL:  srv.URL + "/ctl/IPConn",
			ServiceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pub, ttl, err := c.Map(ctx, 4242)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if pub.String() != "203.0.113.5:4242" {
		t.Errorf("public = %s, want 203.0.113.5:4242", pub)
	}
	if ttl != 7200*time.Second {
		t.Errorf("ttl = %v", ttl)
	}
}

// TestUPnP_Map_RetriesOnPermanentLeases verifies the 725 fallback —
// some routers reject any non-zero lease and want lease=0 (indefinite).
func TestUPnP_Map_RetriesOnPermanentLeases(t *testing.T) {
	attempts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/ctl/IPConn", func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPAction")
		if strings.Contains(action, "AddPortMapping") {
			attempts++
			if attempts == 1 {
				w.Header().Set("Content-Type", `text/xml`)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope><s:Body><s:Fault><faultcode>s:Client</faultcode>
<detail><UPnPError><errorCode>725</errorCode>
<errorDescription>OnlyPermanentLeasesSupported</errorDescription></UPnPError></detail>
</s:Fault></s:Body></s:Envelope>`))
				return
			}
			// 2nd attempt: success
			w.Header().Set("Content-Type", `text/xml`)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope><s:Body><u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/></s:Body></s:Envelope>`))
			return
		}
		if strings.Contains(action, "GetExternalIPAddress") {
			w.Header().Set("Content-Type", `text/xml`)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope><s:Body><u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
<NewExternalIPAddress>203.0.113.5</NewExternalIPAddress>
</u:GetExternalIPAddressResponse></s:Body></s:Envelope>`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &UPnP{
		service: &upnpService{
			ControlURL:  srv.URL + "/ctl/IPConn",
			ServiceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := c.Map(ctx, 4242); err != nil {
		t.Fatalf("Map: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (initial 725 + retry with lease=0)", attempts)
	}
}

// TestUPnP_Map_RejectsPrivateExternalIP catches double-NAT — the
// router reports a private RFC 1918 address as our "external" IP,
// which means our outer ISP NAT will still drop incoming packets.
func TestUPnP_Map_RejectsPrivateExternalIP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ctl/IPConn", func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPAction")
		if strings.Contains(action, "AddPortMapping") {
			w.Header().Set("Content-Type", `text/xml`)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope><s:Body><u:AddPortMappingResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1"/></s:Body></s:Envelope>`))
			return
		}
		if strings.Contains(action, "GetExternalIPAddress") {
			w.Header().Set("Content-Type", `text/xml`)
			_, _ = w.Write([]byte(`<?xml version="1.0"?>
<s:Envelope><s:Body><u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
<NewExternalIPAddress>10.0.0.5</NewExternalIPAddress>
</u:GetExternalIPAddressResponse></s:Body></s:Envelope>`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &UPnP{
		service: &upnpService{
			ControlURL:  srv.URL + "/ctl/IPConn",
			ServiceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := c.Map(ctx, 4242)
	if err == nil {
		t.Fatal("expected error for private external IP")
	}
	if !strings.Contains(err.Error(), "double-NAT") {
		t.Errorf("expected double-NAT mention, got %v", err)
	}
}

func TestUPnP_Unmap_NoServiceIsNoOp(t *testing.T) {
	c := NewUPnP() // service nil
	if err := c.Unmap(context.Background(), 4242); err != nil {
		t.Errorf("Unmap on uninitialized client should be no-op, got %v", err)
	}
}
