// Copyright (c) 2024 Elaunira
// SPDX-License-Identifier: MPL-2.0

// Package mongodb provides an OpenBao database plugin for MongoDB.
package mongodb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	log "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-secure-stdlib/strutil"
	"github.com/openbao/openbao/sdk/v2/database/dbplugin/v5"
	"github.com/openbao/openbao/sdk/v2/database/helper/dbutil"
	"github.com/openbao/openbao/sdk/v2/helper/template"
	"github.com/openbao/openbao/sdk/v2/logical"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
)

const (
	mongoDBTypeName = "mongodb"

	defaultUserNameTemplate = `{{ printf "v-%s-%s-%s-%s" (.DisplayName | truncate 15) (.RoleName | truncate 15) (random 20) (unix_time) | replace "." "-" | truncate 100 }}`

	defaultAuthDatabase = "admin"
)

var _ dbplugin.Database = (*MongoDB)(nil)

// UsernameMetadata holds the metadata used for username generation.
type UsernameMetadata struct {
	DisplayName string
	RoleName    string
}

// MongoDB is the database plugin implementation for MongoDB.
type MongoDB struct {
	*mongoDBConnectionProducer
	usernameProducer template.StringTemplate
	version          string
}

// New returns a new MongoDB instance with the provided username template and version.
func New(usernameTemplate, version string) func() (interface{}, error) {
	return func() (interface{}, error) {
		if usernameTemplate == "" {
			usernameTemplate = defaultUserNameTemplate
		}

		up, err := template.NewTemplate(template.Template(usernameTemplate))
		if err != nil {
			return nil, fmt.Errorf("failed to parse username template: %w", err)
		}

		db := &MongoDB{
			mongoDBConnectionProducer: &mongoDBConnectionProducer{
				Type: mongoDBTypeName,
			},
			usernameProducer: up,
			version:          version,
		}

		wrapped := dbplugin.NewDatabaseErrorSanitizerMiddleware(db, db.secretValues)

		return wrapped, nil
	}
}

// DefaultUserNameTemplate returns the default username template.
func DefaultUserNameTemplate() string {
	return defaultUserNameTemplate
}

// Type returns the type of the database plugin.
func (m *MongoDB) Type() (string, error) {
	return mongoDBTypeName, nil
}

// Metadata returns the plugin metadata.
func (m *MongoDB) Metadata() (map[string]interface{}, error) {
	return map[string]interface{}{
		"version": m.version,
		"type":    mongoDBTypeName,
	}, nil
}

// PluginVersion returns the version of the plugin.
func (m *MongoDB) PluginVersion() logical.PluginVersion {
	return logical.PluginVersion{
		Version: m.version,
	}
}

// Initialize initializes the database plugin with the provided configuration.
func (m *MongoDB) Initialize(ctx context.Context, req dbplugin.InitializeRequest) (dbplugin.InitializeResponse, error) {
	m.Lock()
	defer m.Unlock()

	m.RawConfig = req.Config

	usernameTemplate, err := strutil.GetString(req.Config, "username_template")
	if err != nil {
		return dbplugin.InitializeResponse{}, fmt.Errorf("failed to retrieve username_template: %w", err)
	}
	if usernameTemplate == "" {
		usernameTemplate = defaultUserNameTemplate
	}

	up, err := template.NewTemplate(template.Template(usernameTemplate))
	if err != nil {
		return dbplugin.InitializeResponse{}, fmt.Errorf("unable to initialize username template: %w", err)
	}
	m.usernameProducer = up

	_, err = m.usernameProducer.Generate(dbplugin.UsernameMetadata{})
	if err != nil {
		return dbplugin.InitializeResponse{}, fmt.Errorf("invalid username template: %w", err)
	}

	err = m.loadConfig(req.Config)
	if err != nil {
		return dbplugin.InitializeResponse{}, err
	}

	// Set initialized to true at this point since all fields are set,
	// and the connection can be established at a later time.
	m.Initialized = true

	if req.VerifyConnection {
		client, err := m.createClient(ctx)
		if err != nil {
			return dbplugin.InitializeResponse{}, fmt.Errorf("failed to verify connection: %w", err)
		}

		err = client.Ping(ctx, readpref.Primary())
		if err != nil {
			_ = client.Disconnect(ctx) // Try to prevent any sort of resource leak
			return dbplugin.InitializeResponse{}, fmt.Errorf("failed to verify connection: %w", err)
		}
		m.client = client
	}

	resp := dbplugin.InitializeResponse{
		Config: req.Config,
	}
	return resp, nil
}

// NewUser creates a new user in the MongoDB database.
func (m *MongoDB) NewUser(ctx context.Context, req dbplugin.NewUserRequest) (dbplugin.NewUserResponse, error) {
	if len(req.Statements.Commands) == 0 {
		return dbplugin.NewUserResponse{}, dbutil.ErrEmptyCreationStatement
	}

	username, err := m.usernameProducer.Generate(req.UsernameConfig)
	if err != nil {
		return dbplugin.NewUserResponse{}, err
	}

	// Unmarshal statements.CreationStatements into mongodbRoles
	var mongoCS mongoDBStatement
	err = json.Unmarshal([]byte(req.Statements.Commands[0]), &mongoCS)
	if err != nil {
		return dbplugin.NewUserResponse{}, err
	}

	// Default to "admin" if no db provided
	if mongoCS.DB == "" {
		mongoCS.DB = defaultAuthDatabase
	}

	if len(mongoCS.Roles) == 0 {
		return dbplugin.NewUserResponse{}, fmt.Errorf("roles array is required in creation statement")
	}

	createUserCmd := createUserCommand{
		Username: username,
		Password: req.Password,
		Roles:    mongoCS.Roles.toStandardRolesArray(),
	}

	if err := m.runCommandWithRetry(ctx, mongoCS.DB, createUserCmd); err != nil {
		return dbplugin.NewUserResponse{}, err
	}

	resp := dbplugin.NewUserResponse{
		Username: username,
	}
	return resp, nil
}

// UpdateUser updates an existing user in the MongoDB database.
func (m *MongoDB) UpdateUser(ctx context.Context, req dbplugin.UpdateUserRequest) (dbplugin.UpdateUserResponse, error) {
	if req.Password != nil {
		err := m.changeUserPassword(ctx, req.Username, req.Password.NewPassword)
		return dbplugin.UpdateUserResponse{}, err
	}
	return dbplugin.UpdateUserResponse{}, nil
}

func (m *MongoDB) changeUserPassword(ctx context.Context, username, password string) error {
	connURL := m.getConnectionURL()
	cs, err := connstring.Parse(connURL)
	if err != nil {
		return err
	}

	// Currently doesn't support custom statements for changing the user's password
	changeUserCmd := &updateUserCommand{
		Username: username,
		Password: password,
	}

	database := cs.Database
	if database == "" {
		database = defaultAuthDatabase
	}

	err = m.runCommandWithRetry(ctx, database, changeUserCmd)
	if err != nil {
		return err
	}

	return nil
}

// DeleteUser deletes a user from the MongoDB database.
func (m *MongoDB) DeleteUser(ctx context.Context, req dbplugin.DeleteUserRequest) (dbplugin.DeleteUserResponse, error) {
	// If no revocation statements provided, pass in empty JSON
	var revocationStatement string
	switch len(req.Statements.Commands) {
	case 0:
		revocationStatement = `{}`
	case 1:
		revocationStatement = req.Statements.Commands[0]
	default:
		return dbplugin.DeleteUserResponse{}, fmt.Errorf("expected 0 or 1 revocation statements, got %d", len(req.Statements.Commands))
	}

	// Unmarshal revocation statements into mongodbRoles
	var mongoCS mongoDBStatement
	err := json.Unmarshal([]byte(revocationStatement), &mongoCS)
	if err != nil {
		return dbplugin.DeleteUserResponse{}, err
	}

	db := mongoCS.DB
	// If db is not specified, use the default authenticationDatabase
	if db == "" {
		db = defaultAuthDatabase
	}

	// Set the write concern. The default is majority.
	wc := writeconcern.Majority()
	customWC, err := m.buildWriteConcern()
	if err != nil {
		return dbplugin.DeleteUserResponse{}, err
	}
	if customWC != nil {
		wc = customWC
	}

	dropUserCmd := &dropUserCommand{
		Username:     req.Username,
		WriteConcern: wc,
	}

	err = m.runCommandWithRetry(ctx, db, dropUserCmd)
	var cErr mongo.CommandError
	if errors.As(err, &cErr) && cErr.Name == "UserNotFound" {
		// User already removed, don't retry needlessly
		log.Default().Warn("MongoDB user was deleted prior to lease revocation", "user", req.Username)
		return dbplugin.DeleteUserResponse{}, nil
	}

	return dbplugin.DeleteUserResponse{}, err
}

// runCommandWithRetry runs a command and retries once more if there's a failure
// on the first attempt. This should be called with the lock held.
func (m *MongoDB) runCommandWithRetry(ctx context.Context, db string, cmd interface{}) error {
	// Get the client
	client, err := m.Connection(ctx)
	if err != nil {
		return err
	}

	// Run command
	result := client.Database(db).RunCommand(ctx, cmd, nil)

	// Error check on the first attempt
	err = result.Err()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF), strings.Contains(err.Error(), "EOF"):
		// Call getConnection to reset and retry query if we get an EOF error on first attempt.
		client, err = m.Connection(ctx)
		if err != nil {
			return err
		}
		result = client.Database(db).RunCommand(ctx, cmd, nil)
		if err := result.Err(); err != nil {
			return err
		}
	default:
		return err
	}

	return nil
}

// Close closes the database connection.
func (m *MongoDB) Close() error {
	m.Lock()
	defer m.Unlock()

	return m.mongoDBConnectionProducer.Close()
}

// secretValues returns sensitive values for masking in logs.
func (m *MongoDB) secretValues() map[string]string {
	return map[string]string{
		m.Password: "[password]",
	}
}
