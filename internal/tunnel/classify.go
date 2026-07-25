package tunnel

import (
	"errors"

	"golang.ngrok.com/ngrok/v2"
)

type errClass int

const (
	retryable errClass = iota // default for unrecognized/network errors
	fatal                     // matched a known-bad code; still retried, just with a longer backoff
)

// fatalCodes are ngrok.Error codes verified against ngrok's published error
// reference (https://ngrok.com/docs/errors/reference) as unrecoverable by
// simply retrying — each needs an operator to fix a credential, config
// value, or account-level constraint before a retry could ever succeed.
var fatalCodes = map[string]bool{
	// Authtoken problems — the credential itself is wrong, expired-format,
	// or revoked; nothing about retrying changes that.
	"ERR_NGROK_105": true, // authtoken does not match the proper ngrok authtoken format
	"ERR_NGROK_106": true, // authtoken is a v1 authtoken, used against v2
	"ERR_NGROK_107": true, // authtoken is properly formed but invalid (reset/removed/revoked)
	"ERR_NGROK_300": true, // authtoken credential has been revoked

	// Domain/endpoint reservation & config mismatches — config.yaml's URL
	// or bindings reference a domain/hostname that was never reserved, or
	// is reserved by someone else; needs an ops fix, not a retry.
	"ERR_NGROK_307": true, // address must be reserved for this account before use
	"ERR_NGROK_309": true, // address is reserved for another account
	"ERR_NGROK_318": true, // wildcard domain must be reserved before use
	"ERR_NGROK_319": true, // custom hostname must be reserved before creating endpoints
	"ERR_NGROK_320": true, // domain is reserved for another account
	"ERR_NGROK_322": true, // name is reserved in a different region
	"ERR_NGROK_354": true, // nested subdomains of ngrok base domains must be reserved first
	"ERR_NGROK_381": true, // host:port requested is already reserved by an edge

	// Account/plan limits — bounded by the account's plan or current usage
	// elsewhere; this install retrying rapidly doesn't change that.
	"ERR_NGROK_108": true, // account limited to N simultaneous agent sessions
	"ERR_NGROK_310": true, // plan doesn't allow endpoints with reserved addresses
	"ERR_NGROK_313": true, // plan doesn't allow custom subdomains
	"ERR_NGROK_314": true, // plan doesn't allow custom hostnames
	"ERR_NGROK_315": true, // wildcard domains not available on this plan
	"ERR_NGROK_324": true, // account limited to N endpoints over a single agent
	"ERR_NGROK_348": true, // account limited to N sessions
	"ERR_NGROK_350": true, // account limited to N endpoints in a session

	// Payment/account status — suspended or unpaid; only ops fixing billing
	// resolves this.
	"ERR_NGROK_102": true, // last payment for the account failed
	"ERR_NGROK_103": true, // account has been suspended
	"ERR_NGROK_247": true, // suspended for non-payment

	// TLS/protocol mismatches — config.yaml's URL scheme or TLS
	// termination settings conflict with what the account/edge supports.
	"ERR_NGROK_312": true, // failed to create a TLS endpoint for this account
	"ERR_NGROK_345": true, // HTTPS module TLS termination incompatible with 'tls' tunnel
	"ERR_NGROK_346": true, // HTTPS module lacks TLS termination, incompatible with 'https' tunnel
}

// classify never returns a class that stops retries outright — an
// unattended fleet device must self-heal (e.g. a credential rotated on
// ops's side) without a re-push, so "fatal" only ever means "back off
// harder," never "give up."
func classify(err error) errClass {
	var nerr ngrok.Error
	if errors.As(err, &nerr) {
		if fatalCodes[nerr.Code()] {
			return fatal
		}
	}
	return retryable
}
