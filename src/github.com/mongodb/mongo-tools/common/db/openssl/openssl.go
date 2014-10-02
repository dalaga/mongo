// Package openssl implements connection to MongoDB over ssl.
package openssl

import (
	"fmt"
	"net"
	"time"

	"gopkg.in/mgo.v2"

	"github.com/mongodb/mongo-tools/common/options"
	"github.com/mongodb/mongo-tools/common/util"
	"github.com/spacemonkeygo/openssl"
)

var (
	DefaultSSLDialTimeout = time.Second * 3
)

// For connecting to the database over ssl
type SSLDBConnector struct {
	dialInfo  *mgo.DialInfo
	dialError error
	ctx       *openssl.Ctx
}

// Configure the connector to connect to the server over ssl. Parses the
// connection string, and sets up the correct function to dial the server
// based on the ssl options passed in.
func (self *SSLDBConnector) Configure(opts options.ToolOptions) error {

	// create the addresses to be used to connect
	connectionAddrs := util.CreateConnectionAddrs(opts.Host, opts.Port)

	var err error
	self.ctx, err = setupCtx(opts)
	if err != nil {
		return fmt.Errorf("setupCtx: %v", err)
	}

	var flags openssl.DialFlags
	flags = 0
	if opts.SSLAllowInvalidCert || opts.SSLAllowInvalidHost || opts.SSLCAFile == "" {
		flags = openssl.InsecureSkipHostVerification
	}
	// create the dialer func that will be used to connect
	dialer := func(addr *mgo.ServerAddr) (net.Conn, error) {
		conn, err := openssl.Dial("tcp", addr.String(), self.ctx, flags)
		self.dialError = err
		return conn, err
	}

	// set up the dial info
	self.dialInfo = &mgo.DialInfo{
		Addrs:      connectionAddrs,
		Timeout:    DefaultSSLDialTimeout,
		DialServer: dialer,

		Username:  opts.Auth.Username,
		Password:  opts.Auth.Password,
		Source:    opts.GetAuthenticationDatabase(),
		Mechanism: opts.Auth.Mechanism,
	}

	return nil

}

// Dial the server.
func (self *SSLDBConnector) GetNewSession() (*mgo.Session, error) {
	session, err := mgo.DialWithInfo(self.dialInfo)
	if err != nil && self.dialError != nil {
		return nil, fmt.Errorf("%v, openssl error: %v", err, self.dialError)
	}
	return session, err
}

// To be handed to mgo.DialInfo for connecting to the server.
type dialerFunc func(addr *mgo.ServerAddr) (net.Conn, error)

// Creates and configures an openssl.Ctx
func setupCtx(opts options.ToolOptions) (*openssl.Ctx, error) {
	var ctx *openssl.Ctx
	var err error

	if err := openssl.FIPSModeSet(opts.SSLFipsMode); err != nil {
		return nil, fmt.Errorf("couldn't set FIPS mode to %v: %v", opts.SSLFipsMode, err)
	}

	if ctx, err = openssl.NewCtxWithVersion(openssl.AnyVersion); err != nil {
		return nil, fmt.Errorf("failure creating new openssl context with "+
			"NewCtxWithVersion(AnyVersion): %v", err)
	}

	// OpAll - Activate all bug workaround options, to support buggy client SSL's.
	// NoSSLv2 - Disable SSL v2 support
	ctx.SetOptions(openssl.OpAll | openssl.NoSSLv2)

	// HIGH - Enable strong ciphers
	// !EXPORT - Disable export ciphers (40/56 bit)
	// !aNULL - Disable anonymous auth ciphers
	// @STRENGTH - Sort ciphers based on strength
	ctx.SetCipherList("HIGH:!EXPORT:!aNULL@STRENGTH")

	// add the PEM key file with the cert and private key, if specified
	if opts.SSLPEMKeyFile != "" {
		if err = ctx.UseCertificateChainFile(opts.SSLPEMKeyFile); err != nil {
			return nil, fmt.Errorf("UseCertificateChainFile: %v", err)
		}
		if opts.SSLPEMKeyPassword != "" {
			if err = ctx.UsePrivateKeyFileWithPassword(
				opts.SSLPEMKeyFile, openssl.FiletypePEM, opts.SSLPEMKeyPassword); err != nil {
				return nil, fmt.Errorf("UsePrivateKeyFile: %v", err)
			}
		} else {
			if err = ctx.UsePrivateKeyFile(opts.SSLPEMKeyFile, openssl.FiletypePEM); err != nil {
				return nil, fmt.Errorf("UsePrivateKeyFile: %v", err)
			}
		}
		// Verify that the certificate and the key go together.
		if err = ctx.CheckPrivateKey(); err != nil {
			return nil, fmt.Errorf("CheckPrivateKey: %v", err)
		}
	}

	// If renegotiation is needed, don't return from recv() or send() until it's successful.
	// Note: this is for blocking sockets only.
	ctx.SetMode(openssl.AutoRetry)

	// Disable session caching (see SERVER-10261)
	ctx.SetSessionCacheMode(openssl.SessionCacheOff)

	if opts.SSLCAFile != "" {
		calist, err := openssl.LoadClientCAFile(opts.SSLCAFile)
		if err != nil {
			return nil, fmt.Errorf("LoadClientCAFile: %v", err)
		}
		ctx.SetClientCAList(calist)

		if err = ctx.LoadVerifyLocations(opts.SSLCAFile, ""); err != nil {
			return nil, fmt.Errorf("LoadVerifyLocations: %v", err)
		}

		var verifyOption openssl.VerifyOptions
		if opts.SSLAllowInvalidCert {
			verifyOption = openssl.VerifyNone
		} else {
			verifyOption = openssl.VerifyPeer
		}
		ctx.SetVerify(verifyOption, nil)
	}

	if opts.SSLCRLFile != "" {
		store := ctx.GetCertificateStore()
		store.SetFlags(openssl.CRLCheck)
		lookup, err := store.AddLookup(openssl.X509LookupFile())
		if err != nil {
			return nil, fmt.Errorf("AddLookup(X509LookupFile()): %v", err)
		}
		lookup.LoadCRLFile(opts.SSLCRLFile)
	}

	return ctx, nil
}
