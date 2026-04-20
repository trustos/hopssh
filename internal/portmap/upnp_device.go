package portmap

// UPnP device description fetch + service tree walk. The SSDP reply's
// LOCATION header points at an XML document describing the device's
// service tree. We find a WANConnection service (WANIPConnection
// preferred, WANPPPConnection fallback) and extract its controlURL
// for SOAP requests.
//
// We use encoding/xml with minimal struct definitions — the spec
// allows arbitrary nesting depth, but in practice IGD devices follow
// a fixed shape (root → device → deviceList → device → serviceList →
// service). We walk recursively to handle both.

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Service types we accept, in preference order. Both v1 and v2 forms.
var wanServiceTypes = []string{
	"urn:schemas-upnp-org:service:WANIPConnection:2",
	"urn:schemas-upnp-org:service:WANIPConnection:1",
	"urn:schemas-upnp-org:service:WANPPPConnection:1",
}

// upnpService is the resolved info needed to make SOAP calls.
type upnpService struct {
	// ControlURL is the absolute HTTP URL for SOAP action POSTs.
	ControlURL string
	// ServiceType is the urn that the SOAPAction header must reference.
	ServiceType string
}

// xmlDevice is a partial mapping of the IGD device description XML.
// Only the fields we walk are declared — extra elements are ignored
// by encoding/xml.
type xmlDevice struct {
	XMLName     xml.Name      `xml:"device"`
	DeviceType  string        `xml:"deviceType"`
	ServiceList xmlServiceList `xml:"serviceList"`
	DeviceList  xmlDeviceList  `xml:"deviceList"`
}

type xmlDeviceList struct {
	Devices []xmlDevice `xml:"device"`
}

type xmlServiceList struct {
	Services []xmlService `xml:"service"`
}

type xmlService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
	SCPDURL     string `xml:"SCPDURL"`
}

type xmlRoot struct {
	XMLName xml.Name  `xml:"root"`
	URLBase string    `xml:"URLBase"`
	Device  xmlDevice `xml:"device"`
}

// fetchService downloads the device description XML at locationURL and
// returns the highest-preference WAN-connection service. errNoService
// if the device description has none.
func fetchService(ctx context.Context, locationURL string) (*upnpService, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, locationURL, nil)
	if err != nil {
		return nil, fmt.Errorf("upnp: build GET %s: %w", locationURL, err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upnp: GET %s: %w", locationURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upnp: GET %s: status %d", locationURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap — IGD descriptions are 5-30 KB
	if err != nil {
		return nil, fmt.Errorf("upnp: read body: %w", err)
	}

	var root xmlRoot
	if err := xml.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("upnp: parse xml: %w", err)
	}

	// Resolve the base URL for relative controlURLs. URLBase if present;
	// otherwise the directory containing the LOCATION URL.
	base, err := resolveBaseURL(locationURL, root.URLBase)
	if err != nil {
		return nil, err
	}

	svc := findPreferredService(&root.Device)
	if svc == nil {
		return nil, errNoService
	}
	abs, err := absURL(base, svc.ControlURL)
	if err != nil {
		return nil, err
	}
	return &upnpService{
		ControlURL:  abs,
		ServiceType: svc.ServiceType,
	}, nil
}

// findPreferredService walks the device tree depth-first, returning the
// first service whose type matches our preference list (in order).
func findPreferredService(d *xmlDevice) *xmlService {
	if d == nil {
		return nil
	}
	for _, want := range wanServiceTypes {
		if s := findServiceOfType(d, want); s != nil {
			return s
		}
	}
	return nil
}

func findServiceOfType(d *xmlDevice, serviceType string) *xmlService {
	for i := range d.ServiceList.Services {
		s := &d.ServiceList.Services[i]
		if s.ServiceType == serviceType {
			return s
		}
	}
	for i := range d.DeviceList.Devices {
		if s := findServiceOfType(&d.DeviceList.Devices[i], serviceType); s != nil {
			return s
		}
	}
	return nil
}

// resolveBaseURL picks the right base URL for relative-path resolution.
// If URLBase is given in the device description, that wins; otherwise
// we use the directory part of the LOCATION URL.
func resolveBaseURL(locationURL, urlBase string) (*url.URL, error) {
	if strings.TrimSpace(urlBase) != "" {
		u, err := url.Parse(urlBase)
		if err == nil {
			return u, nil
		}
	}
	loc, err := url.Parse(locationURL)
	if err != nil {
		return nil, fmt.Errorf("upnp: parse location: %w", err)
	}
	return loc, nil
}

func absURL(base *url.URL, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("upnp: empty controlURL")
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("upnp: parse controlURL %q: %w", ref, err)
	}
	return base.ResolveReference(r).String(), nil
}

var errNoService = errors.New("upnp: no WAN connection service in device description")
