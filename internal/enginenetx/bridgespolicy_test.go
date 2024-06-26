package enginenetx

import (
	"context"
	"errors"
	"testing"

	"github.com/ooni/probe-cli/v3/internal/mocks"
	"github.com/ooni/probe-cli/v3/internal/model"
)

func TestBridgesPolicy(t *testing.T) {
	t.Run("for domains for which we don't have bridges and DNS failure", func(t *testing.T) {
		expected := errors.New("mocked error")
		p := &bridgesPolicy{
			Fallback: &dnsPolicy{
				Logger: model.DiscardLogger,
				Resolver: &mocks.Resolver{
					MockLookupHost: func(ctx context.Context, domain string) ([]string, error) {
						return nil, expected
					},
				},
			},
		}

		ctx := context.Background()
		tactics := p.LookupTactics(ctx, "www.example.com", "443")

		var count int
		for range tactics {
			count++
		}

		if count != 0 {
			t.Fatal("expected to see zero tactics")
		}
	})

	t.Run("for domains for which we don't have bridges and DNS success", func(t *testing.T) {
		p := &bridgesPolicy{
			Fallback: &dnsPolicy{
				Logger: model.DiscardLogger,
				Resolver: &mocks.Resolver{
					MockLookupHost: func(ctx context.Context, domain string) ([]string, error) {
						return []string{"93.184.216.34"}, nil
					},
				},
			},
		}

		ctx := context.Background()
		tactics := p.LookupTactics(ctx, "www.example.com", "443")

		var count int
		for tactic := range tactics {
			count++

			if tactic.Port != "443" {
				t.Fatal("the port should always be 443")
			}
			if tactic.Address != "93.184.216.34" {
				t.Fatal("the host should always be 93.184.216.34")
			}

			if tactic.InitialDelay != 0 {
				t.Fatal("unexpected InitialDelay")
			}

			if tactic.SNI != "www.example.com" {
				t.Fatal("the SNI field should always be like `www.example.com`")
			}

			if tactic.VerifyHostname != "www.example.com" {
				t.Fatal("the VerifyHostname field should always be like `www.example.com`")
			}
		}

		if count != 1 {
			t.Fatal("expected to see one tactic")
		}
	})

	t.Run("for the api.ooni.io domain with DNS failure", func(t *testing.T) {
		expected := errors.New("mocked error")
		p := &bridgesPolicy{
			Fallback: &dnsPolicy{
				Logger: model.DiscardLogger,
				Resolver: &mocks.Resolver{
					MockLookupHost: func(ctx context.Context, domain string) ([]string, error) {
						return nil, expected
					},
				},
			},
		}

		ctx := context.Background()
		tactics := p.LookupTactics(ctx, "api.ooni.io", "443")

		// since the DNS fails, we should only see tactics generated by bridges
		var count int
		for tactic := range tactics {
			count++

			if tactic.Port != "443" {
				t.Fatal("the port should always be 443")
			}
			if tactic.Address != "162.55.247.208" {
				t.Fatal("the host should always be 162.55.247.208")
			}

			if tactic.InitialDelay != 0 {
				t.Fatal("unexpected InitialDelay")
			}

			if tactic.SNI == "api.ooni.io" {
				t.Fatal("we should not see the `api.ooni.io` SNI on the wire")
			}

			if tactic.VerifyHostname != "api.ooni.io" {
				t.Fatal("the VerifyHostname field should always be like `api.ooni.io`")
			}
		}

		if count <= 0 {
			t.Fatal("expected to see at least one tactic")
		}
	})

	t.Run("for the api.ooni.io domain with DNS success", func(t *testing.T) {
		p := &bridgesPolicy{
			Fallback: &dnsPolicy{
				Logger: model.DiscardLogger,
				Resolver: &mocks.Resolver{
					MockLookupHost: func(ctx context.Context, domain string) ([]string, error) {
						return []string{"130.192.91.211"}, nil
					},
				},
			},
		}

		ctx := context.Background()
		tactics := p.LookupTactics(ctx, "api.ooni.io", "443")

		// since the DNS succeeds we should see bridge tactics mixed with DNS tactics
		var (
			bridgesCount int
			dnsCount     int
			overallCount int
		)
		const expectedDNSEntryCount = 153 // yikes!
		for tactic := range tactics {
			overallCount++

			t.Log(overallCount, tactic)

			if tactic.Port != "443" {
				t.Fatal("the port should always be 443")
			}

			switch {
			case overallCount == expectedDNSEntryCount:
				if tactic.Address != "130.192.91.211" {
					t.Fatal("the host should be 130.192.91.211 for count ==", expectedDNSEntryCount)
				}

				if tactic.SNI != "api.ooni.io" {
					t.Fatal("we should see the `api.ooni.io` SNI on the wire for count ==", expectedDNSEntryCount)
				}

				dnsCount++

			default:
				if tactic.Address != "162.55.247.208" {
					t.Fatal("the host should be 162.55.247.208 for count !=", expectedDNSEntryCount)
				}

				if tactic.SNI == "api.ooni.io" {
					t.Fatal("we should not see the `api.ooni.io` SNI on the wire for count !=", expectedDNSEntryCount)
				}

				bridgesCount++
			}

			if tactic.InitialDelay != 0 {
				t.Fatal("unexpected InitialDelay")
			}

			if tactic.VerifyHostname != "api.ooni.io" {
				t.Fatal("the VerifyHostname field should always be like `api.ooni.io`")
			}
		}

		if overallCount <= 0 {
			t.Fatal("expected to see at least one tactic")
		}
		if dnsCount != 1 {
			t.Fatal("expected to see exactly one DNS based tactic")
		}
		if bridgesCount <= 0 {
			t.Fatal("expected to see at least one bridge tactic")
		}
	})

	t.Run("for test helper domains", func(t *testing.T) {
		for _, domain := range bridgesPolicyTestHelpersDomains {
			t.Run(domain, func(t *testing.T) {
				expectedAddrs := []string{"164.92.180.7"}

				p := &bridgesPolicy{
					Fallback: &dnsPolicy{
						Logger: model.DiscardLogger,
						Resolver: &mocks.Resolver{
							MockLookupHost: func(ctx context.Context, domain string) ([]string, error) {
								return expectedAddrs, nil
							},
						},
					},
				}

				ctx := context.Background()
				for tactic := range p.LookupTactics(ctx, domain, "443") {

					if tactic.Address != "164.92.180.7" {
						t.Fatal("unexpected .Address")
					}

					if tactic.InitialDelay != 0 {
						t.Fatal("unexpected .InitialDelay")
					}

					if tactic.Port != "443" {
						t.Fatal("unexpected .Port")
					}

					if tactic.SNI == domain {
						t.Fatal("unexpected .Domain")
					}

					if tactic.VerifyHostname != domain {
						t.Fatal("unexpected .VerifyHostname")
					}
				}
			})
		}
	})
}
