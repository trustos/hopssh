package portmap

// SOAP envelope build + parse for the two IGD actions we use:
//   AddPortMapping       — request a router port forwarding
//   DeletePortMapping    — remove one
//   GetExternalIPAddress — learn the public IP
//
// We hand-build the envelopes (text/template via fmt.Sprintf) and
// parse responses with regex/encoding-xml. Avoids pulling in an XML
// generation library for fixed-shape requests.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Common UPnP error codes we have specific handling for. From the
// WANIPConnection:1 service template.
const (
	upnpErrInvalidArgs              = 402
	upnpErrStringTooLong            = 605
	upnpErrWildcardSrcIPNotPermitted = 715
	upnpErrConflictMappingEntry     = 718
	upnpErrOnlyPermanentLeases      = 725
)

// soapErr is returned when the IGD replied with a SOAP fault. Code is
// the UPnPError errorCode if extractable, 0 otherwise.
type soapErr struct {
	HTTPStatus int
	Code       int
	Detail     string
}

func (e *soapErr) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("upnp: SOAP fault %d (%s)", e.Code, e.Detail)
	}
	return fmt.Sprintf("upnp: HTTP %d: %s", e.HTTPStatus, e.Detail)
}

// soapCall posts the SOAP envelope to controlURL and returns the
// raw response body on 200, or *soapErr on non-200 / SOAP fault.
func soapCall(ctx context.Context, controlURL, serviceType, action, body string) ([]byte, error) {
	envelope := buildSOAPEnvelope(serviceType, action, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL, bytes.NewReader(envelope))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s#%s"`, serviceType, action))
	req.ContentLength = int64(len(envelope))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upnp: POST %s: %w", controlURL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusOK {
		return respBody, nil
	}
	// Non-200: try to extract <errorCode>.
	se := &soapErr{HTTPStatus: resp.StatusCode, Detail: strings.TrimSpace(string(respBody))}
	if code := extractErrorCode(respBody); code != 0 {
		se.Code = code
	}
	return nil, se
}

func buildSOAPEnvelope(serviceType, action, innerBody string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0"?>`+"\n"+
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" `+
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">`+"\n"+
		`<s:Body>`+"\n"+
		`<u:%s xmlns:u="%s">`+"\n%s\n"+
		`</u:%s>`+"\n"+
		`</s:Body>`+"\n"+
		`</s:Envelope>`+"\n",
		action, serviceType, innerBody, action))
}

var errorCodeRE = regexp.MustCompile(`<errorCode>\s*(\d+)\s*</errorCode>`)

func extractErrorCode(body []byte) int {
	m := errorCodeRE.FindSubmatch(body)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n
}

// AddPortMapping action body. Field order matters — some routers 402
// on reordered children.
func addPortMappingBody(externalPort, internalPort uint16, internalClient string, lease uint32) string {
	return fmt.Sprintf(
		`<NewRemoteHost></NewRemoteHost>
<NewExternalPort>%d</NewExternalPort>
<NewProtocol>UDP</NewProtocol>
<NewInternalPort>%d</NewInternalPort>
<NewInternalClient>%s</NewInternalClient>
<NewEnabled>1</NewEnabled>
<NewPortMappingDescription>hopssh</NewPortMappingDescription>
<NewLeaseDuration>%d</NewLeaseDuration>`,
		externalPort, internalPort, internalClient, lease)
}

func deletePortMappingBody(externalPort uint16) string {
	return fmt.Sprintf(
		`<NewRemoteHost></NewRemoteHost>
<NewExternalPort>%d</NewExternalPort>
<NewProtocol>UDP</NewProtocol>`,
		externalPort)
}

// GetExternalIPAddress response: <NewExternalIPAddress>x.y.z.w</NewExternalIPAddress>
type extIPResponse struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		Resp struct {
			IP string `xml:"NewExternalIPAddress"`
		} `xml:"GetExternalIPAddressResponse"`
	} `xml:"Body"`
}

func parseGetExternalIP(body []byte) (string, error) {
	var r extIPResponse
	if err := xml.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("upnp: parse GetExternalIPAddress response: %w", err)
	}
	ip := strings.TrimSpace(r.Body.Resp.IP)
	if ip == "" {
		return "", fmt.Errorf("upnp: empty NewExternalIPAddress")
	}
	return ip, nil
}
