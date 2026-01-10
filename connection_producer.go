// Copyright (c) 2024 Elaunira
// SPDX-License-Identifier: MPL-2.0

package mongodb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/openbao/openbao/sdk/v2/database/helper/connutil"
	"github.com/openbao/openbao/sdk/v2/database/helper/dbutil"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
	"go.mongodb.org/mongo-driver/x/mongo/driver/auth"
)

// mongoDBConnectionProducer implements ConnectionProducer and provides an
// interface for databases to make connections.
type mongoDBConnectionProducer struct {
	ConnectionURL string `json:"connection_url" structs:"connection_url" mapstructure:"connection_url"`
	WriteConcern  string `json:"write_concern" structs:"write_concern" mapstructure:"write_concern"`

	Username string `json:"username" structs:"username" mapstructure:"username"`
	Password string `json:"password" structs:"password" mapstructure:"password"`

	TLSCertificateKeyData []byte `json:"tls_certificate_key" structs:"-" mapstructure:"tls_certificate_key"`
	TLSCAData             []byte `json:"tls_ca"              structs:"-" mapstructure:"tls_ca"`

	SocketTimeout          time.Duration `json:"socket_timeout"           structs:"-" mapstructure:"socket_timeout"`
	ConnectTimeout         time.Duration `json:"connect_timeout"          structs:"-" mapstructure:"connect_timeout"`
	ServerSelectionTimeout time.Duration `json:"server_selection_timeout" structs:"-" mapstructure:"server_selection_timeout"`

	Initialized   bool
	RawConfig     map[string]interface{}
	Type          string
	clientOptions *options.ClientOptions
	client        *mongo.Client
	sync.Mutex
}

// writeConcernConfig defines the write concern options.
type writeConcernConfig struct {
	W        int    // Min # of servers to ack before success
	WMode    string // Write mode for MongoDB 2.0+ (e.g. "majority")
	WTimeout int    // Milliseconds to wait for W before timing out
	FSync    bool   // DEPRECATED: Is now handled by J. See: https://jira.mongodb.org/browse/CXX-910
	J        bool   // Sync via the journal if present
}

func (c *mongoDBConnectionProducer) loadConfig(cfg map[string]interface{}) error {
	err := mapstructure.WeakDecode(cfg, c)
	if err != nil {
		return err
	}

	if c.ConnectionURL == "" {
		return fmt.Errorf("connection_url cannot be empty")
	}

	if c.SocketTimeout < 0 {
		return fmt.Errorf("socket_timeout must be >= 0")
	}
	if c.ConnectTimeout < 0 {
		return fmt.Errorf("connect_timeout must be >= 0")
	}
	if c.ServerSelectionTimeout < 0 {
		return fmt.Errorf("server_selection_timeout must be >= 0")
	}

	opts, err := c.makeClientOpts()
	if err != nil {
		return err
	}

	c.clientOptions = opts

	return nil
}

// Connection creates or returns an existing database connection. If the session fails
// on a ping check, the session will be closed and then re-created.
// This method locks the mutex on its own.
func (c *mongoDBConnectionProducer) Connection(ctx context.Context) (*mongo.Client, error) {
	if !c.Initialized {
		return nil, connutil.ErrNotInitialized
	}

	c.Lock()
	defer c.Unlock()

	if c.client != nil {
		if err := c.client.Ping(ctx, readpref.Primary()); err == nil {
			return c.client, nil
		}
		// Ignore error on purpose since we want to re-create a session
		_ = c.client.Disconnect(ctx)
	}

	client, err := c.createClient(ctx)
	if err != nil {
		return nil, err
	}
	c.client = client
	return c.client, nil
}

func (c *mongoDBConnectionProducer) createClient(ctx context.Context) (client *mongo.Client, err error) {
	if !c.Initialized {
		return nil, fmt.Errorf("failed to create client: connection producer is not initialized")
	}
	if c.clientOptions == nil {
		return nil, fmt.Errorf("missing client options")
	}
	// Apply URI first, then overlay with our configured options
	opts := options.Client().ApplyURI(c.getConnectionURL())

	// Re-apply our settings on top of URI-parsed options
	if err := c.applyWriteConcern(opts); err != nil {
		return nil, err
	}
	if err := c.applyTLSAuth(opts); err != nil {
		return nil, err
	}
	c.applyTimeouts(opts)

	client, err = mongo.Connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Close terminates the database connection.
func (c *mongoDBConnectionProducer) Close() error {
	if c.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer cancel()
		if err := c.client.Disconnect(ctx); err != nil {
			return err
		}
	}

	c.client = nil

	return nil
}

func (c *mongoDBConnectionProducer) getConnectionURL() (connURL string) {
	connURL = dbutil.QueryHelper(c.ConnectionURL, map[string]string{
		"username": c.Username,
		"password": c.Password,
	})
	return connURL
}

func (c *mongoDBConnectionProducer) makeClientOpts() (*options.ClientOptions, error) {
	opts := options.Client()

	// Apply write concern settings
	if err := c.applyWriteConcern(opts); err != nil {
		return nil, err
	}

	// Apply TLS and auth settings
	if err := c.applyTLSAuth(opts); err != nil {
		return nil, err
	}

	// Apply timeout settings
	c.applyTimeouts(opts)

	return opts, nil
}

func (c *mongoDBConnectionProducer) applyWriteConcern(opts *options.ClientOptions) error {
	wc, err := c.buildWriteConcern()
	if err != nil {
		return err
	}
	if wc != nil {
		opts.SetWriteConcern(wc)
	}
	return nil
}

// buildWriteConcern parses the write concern configuration and returns a WriteConcern object.
func (c *mongoDBConnectionProducer) buildWriteConcern() (*writeconcern.WriteConcern, error) {
	if c.WriteConcern == "" {
		return nil, nil
	}

	input := c.WriteConcern

	// Try to base64 decode the input. If successful, consider the decoded
	// value as input.
	inputBytes, err := base64.StdEncoding.DecodeString(input)
	if err == nil {
		input = string(inputBytes)
	}

	concern := &writeConcernConfig{}
	err = json.Unmarshal([]byte(input), concern)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling write_concern: %w", err)
	}

	// Build write concern using struct literal (new API)
	wc := &writeconcern.WriteConcern{
		Journal: &concern.J,
	}

	// Set W value
	switch {
	case concern.W != 0:
		wc.W = concern.W
	case concern.WMode != "":
		wc.W = concern.WMode
	}

	// Handle deprecated FSync by mapping to Journal
	if concern.FSync {
		journal := true
		wc.Journal = &journal
	}

	if concern.WTimeout > 0 {
		wc.WTimeout = time.Duration(concern.WTimeout) * time.Millisecond
	}

	return wc, nil
}

func (c *mongoDBConnectionProducer) applyTLSAuth(opts *options.ClientOptions) error {
	if len(c.TLSCAData) == 0 && len(c.TLSCertificateKeyData) == 0 {
		return nil
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if len(c.TLSCAData) > 0 {
		tlsConfig.RootCAs = x509.NewCertPool()

		ok := tlsConfig.RootCAs.AppendCertsFromPEM(c.TLSCAData)
		if !ok {
			return fmt.Errorf("failed to append CA to client options")
		}
	}

	if len(c.TLSCertificateKeyData) > 0 {
		certificate, err := tls.X509KeyPair(c.TLSCertificateKeyData, c.TLSCertificateKeyData)
		authMechanism := auth.MongoDBX509
		if err != nil {
			return fmt.Errorf("unable to load tls_certificate_key_data: %w", err)
		}

		// Ensure SCRAM-SHA-256 is explicitly set if username/password auth is provided
		if c.Username != "" && c.Password != "" {
			authMechanism = auth.SCRAMSHA256
		}

		opts.SetAuth(options.Credential{
			AuthMechanism: authMechanism,
			Username:      c.Username,
			Password:      c.Password,
		})

		tlsConfig.Certificates = append(tlsConfig.Certificates, certificate)
	}

	opts.SetTLSConfig(tlsConfig)
	return nil
}

func (c *mongoDBConnectionProducer) applyTimeouts(opts *options.ClientOptions) {
	if c.SocketTimeout == 0 {
		opts.SetSocketTimeout(1 * time.Minute)
	} else {
		opts.SetSocketTimeout(c.SocketTimeout)
	}

	if c.ConnectTimeout == 0 {
		opts.SetConnectTimeout(1 * time.Minute)
	} else {
		opts.SetConnectTimeout(c.ConnectTimeout)
	}

	if c.ServerSelectionTimeout != 0 {
		opts.SetServerSelectionTimeout(c.ServerSelectionTimeout)
	}
}
