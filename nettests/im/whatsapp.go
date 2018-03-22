package im

import (
	"github.com/measurement-kit/go-measurement-kit"
	"github.com/openobservatory/gooni/nettests"
)

// WhatsApp test implementation
type WhatsApp struct {
}

// Run starts the test
func (h WhatsApp) Run(ctl *nettests.Controller) error {
	mknt := mk.NewNettest("Whatsapp")
	ctl.Init(mknt)
	return mknt.Run()
}

// WhatsAppSummary for the test
type WhatsAppSummary struct {
	RegistrationServerBlocking bool
	WebBlocking                bool
	EndpointsBlocking          bool
}

// Summary generates a summary for a test run
func (h WhatsApp) Summary(tk map[string]interface{}) interface{} {
	const blk = "blocked"

	return WhatsAppSummary{
		RegistrationServerBlocking: tk["registration_server_status"].(string) == blk,
		WebBlocking:                tk["whatsapp_web_status"].(string) == blk,
		EndpointsBlocking:          tk["whatsapp_endpoints_status"].(string) == blk,
	}
}

// LogSummary writes the summary to the standard output
func (h WhatsApp) LogSummary(s string) error {
	return nil
}
