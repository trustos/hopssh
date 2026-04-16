// Package quictransport provides a QUIC datagram endpoint for the hopssh
// control plane. This is the foundation of Phase 1 of the QUIC transport
// migration (see plan/purring-chasing-babbage). It currently exposes a simple
// echo endpoint used by `hop-agent migration` to validate that QUIC connection
// migration keeps a tunnel alive across IP changes (e.g. WiFi → cellular).
//
// Auth model: self-signed TLS cert at startup. This is intentionally minimal
// for the validation phase; production auth via the Nebula PKI is a follow-up
// once we know migration actually works for our use case.
package quictransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/sirupsen/logrus"
)

// ALPN identifies the migration-probe service. Bumped per protocol revision.
const ALPN = "hopssh-quic/v1"

// Server is the QUIC datagram endpoint. Currently it only echoes datagrams
// back to clients (used by hop-agent's migration probe), but this is the
// scaffolding the Phase 1 mesh transport will plug into.
type Server struct {
	listener *quic.Listener
	log      *logrus.Logger

	// Stats for visibility / future metrics.
	connsAccepted atomic.Uint64
	datagramsRX   atomic.Uint64
	datagramsTX   atomic.Uint64
}

// NewServer creates a QUIC listener on the given UDP port. Caller must call
// Run to start accepting connections, and Close to shut down.
func NewServer(port int, log *logrus.Logger) (*Server, error) {
	if log == nil {
		log = logrus.New()
		log.SetLevel(logrus.WarnLevel)
	}
	tlsConf, err := generateSelfSignedTLSConfig()
	if err != nil {
		return nil, fmt.Errorf("generate TLS config: %w", err)
	}

	udpAddr := &net.UDPAddr{IP: net.IPv4zero, Port: port}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen UDP %d: %w", port, err)
	}

	tr := &quic.Transport{Conn: conn}
	ln, err := tr.Listen(tlsConf, &quic.Config{
		EnableDatagrams:      true,
		MaxIdleTimeout:       60 * time.Second,
		KeepAlivePeriod:      15 * time.Second,
		HandshakeIdleTimeout: 10 * time.Second,
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("quic listen: %w", err)
	}

	return &Server{listener: ln, log: log}, nil
}

// Run accepts QUIC connections until ctx is cancelled or Close is called.
// Each connection runs in its own goroutine.
func (s *Server) Run(ctx context.Context) error {
	for {
		conn, err := s.listener.Accept(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			// Log and continue — one bad connection shouldn't kill the listener.
			s.log.WithError(err).Warn("quictransport: Accept failed")
			continue
		}
		s.connsAccepted.Add(1)
		s.log.WithField("remote", conn.RemoteAddr()).Info("quictransport: connection accepted")
		go s.handleConn(ctx, conn)
	}
}

// Close shuts down the listener.
func (s *Server) Close() error {
	if s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

// Stats returns current counters. Useful for /healthz or future metrics.
func (s *Server) Stats() (conns, rx, tx uint64) {
	return s.connsAccepted.Load(), s.datagramsRX.Load(), s.datagramsTX.Load()
}

// handleConn reads datagrams from a single connection. If the datagram starts
// with the magic byte 0xFF, echo it back unchanged (migration-probe contract).
// Other datagrams are silently dropped — they're reserved for future tunneled
// traffic.
func (s *Server) handleConn(parent context.Context, conn *quic.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() {
		s.log.WithField("remote", conn.RemoteAddr()).Info("quictransport: connection closed")
		_ = conn.CloseWithError(0, "bye")
	}()

	for {
		msg, err := conn.ReceiveDatagram(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			// Most errors here are "client went away" — log at debug, return.
			s.log.WithError(err).WithField("remote", conn.RemoteAddr()).Debug("quictransport: ReceiveDatagram error")
			return
		}
		s.datagramsRX.Add(1)

		if len(msg) > 0 && msg[0] == 0xFF {
			if err := conn.SendDatagram(msg); err != nil {
				s.log.WithError(err).Debug("quictransport: SendDatagram echo failed")
				continue
			}
			s.datagramsTX.Add(1)
		}
		// Non-echo datagrams are dropped (reserved for future use).
	}
}

// generateSelfSignedTLSConfig returns a TLS 1.3 config with a fresh ephemeral
// EC P-256 cert. Suitable for the migration-probe phase where we don't yet
// authenticate peers (the QUIC test client uses InsecureSkipVerify).
func generateSelfSignedTLSConfig() (*tls.Config, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "hopssh-quic"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(pemCert, pemKey)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{ALPN},
		MinVersion:   tls.VersionTLS13,
	}, nil
}
