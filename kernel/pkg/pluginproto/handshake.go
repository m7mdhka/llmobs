package pluginproto

import "fmt"

// Info is the plugin's handshake self-report (GET /plugin/v1/info), validated
// against api/plugin/v1alpha1/handshake.schema.json.
type Info struct {
	ID               string   `json:"id"`
	Version          string   `json:"version"`
	PluginAPIVersion string   `json:"pluginApiVersion"`
	Capabilities     []string `json:"capabilities"`
	DisplayName      string   `json:"displayName,omitempty"`
}

// Check validates a plugin's handshake against the authoritative manifest: the id
// must match, the protocol must be supported, and the echoed capabilities must be
// a subset of what the manifest granted (defence in depth — a plugin cannot claim
// more than the manifest, which is the authority). Returns nil when the handshake
// is acceptable and a service token may be issued.
func (i Info) Check(manifestID string, grantedCaps []string) error {
	if i.ID != manifestID {
		return fmt.Errorf("pluginproto: handshake id %q != manifest id %q", i.ID, manifestID)
	}
	if !ProtocolSupported(i.PluginAPIVersion) {
		return fmt.Errorf("pluginproto: unsupported plugin protocol %q (kernel supports %v)", i.PluginAPIVersion, SupportedProtocols)
	}
	grant := make(map[string]struct{}, len(grantedCaps))
	for _, c := range grantedCaps {
		grant[c] = struct{}{}
	}
	for _, c := range i.Capabilities {
		if _, ok := grant[c]; !ok {
			return fmt.Errorf("pluginproto: plugin claims capability %q not granted by manifest", c)
		}
	}
	return nil
}
