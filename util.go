// Copyright (c) 2024 Elaunira
// SPDX-License-Identifier: MPL-2.0

package mongodb

import "go.mongodb.org/mongo-driver/mongo/writeconcern"

// createUserCommand is the MongoDB command to create a user.
type createUserCommand struct {
	Username string        `bson:"createUser"`
	Password string        `bson:"pwd,omitempty"`
	Roles    []interface{} `bson:"roles"`
}

// updateUserCommand is the MongoDB command to update a user's password.
type updateUserCommand struct {
	Username string `bson:"updateUser"`
	Password string `bson:"pwd"`
}

// dropUserCommand is the MongoDB command to drop a user.
type dropUserCommand struct {
	Username     string                     `bson:"dropUser"`
	WriteConcern *writeconcern.WriteConcern `bson:"writeConcern"`
}

// mongodbRole represents a MongoDB role with its associated database.
type mongodbRole struct {
	Role string `json:"role" bson:"role"`
	DB   string `json:"db"   bson:"db"`
}

// mongodbRoles is a slice of mongodbRole.
type mongodbRoles []mongodbRole

// mongoDBStatement represents the creation statement for a MongoDB user.
type mongoDBStatement struct {
	DB    string       `json:"db"`
	Roles mongodbRoles `json:"roles"`
}

// toStandardRolesArray converts mongodbRoles to the format required by MongoDB's createUser command.
// If a role has no DB specified, it returns just the role name as a string.
// Otherwise, it returns the full role object with both role and db fields.
func (roles mongodbRoles) toStandardRolesArray() []interface{} {
	var standardRolesArray []interface{}
	for _, role := range roles {
		if role.DB == "" {
			standardRolesArray = append(standardRolesArray, role.Role)
		} else {
			standardRolesArray = append(standardRolesArray, role)
		}
	}
	return standardRolesArray
}
