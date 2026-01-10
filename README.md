# OpenBao MongoDB Database Plugin

This plugin provides dynamic credential management for MongoDB databases using [OpenBao](https://openbao.org/).

## Features

- Dynamic user creation and deletion
- Password rotation for existing users
- TLS/SSL connection support
- X.509 certificate authentication
- Configurable write concern
- Connection timeout settings

## Installation

### Building from source

```bash
make build
```

This produces a `mongodb-database-plugin` binary.

### Cross-compiling for Linux

```bash
make build-linux
```

### Installing the plugin

```bash
make install
```

This installs the plugin to `/usr/lib/openbao/plugins/`.

## Configuration

### Register the plugin

```bash
# Calculate the SHA256 checksum
SHA256=$(sha256sum mongodb-database-plugin | cut -d' ' -f1)

# Register the plugin
bao plugin register -sha256=$SHA256 database mongodb-database-plugin
```

### Configure the database connection

```bash
bao write database/config/my-mongodb-database \
    plugin_name=mongodb-database-plugin \
    allowed_roles="my-role" \
    connection_url="mongodb://{{username}}:{{password}}@localhost:27017/admin" \
    username="admin" \
    password="adminpassword"
```

### Configuration parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `connection_url` | MongoDB connection URL (required) | - |
| `username` | Username for authentication | - |
| `password` | Password for authentication | - |
| `tls_certificate_key` | Client certificate and key (PEM format) | - |
| `tls_ca` | CA certificate (PEM format) | - |
| `write_concern` | Write concern configuration (JSON) | - |
| `socket_timeout` | Socket timeout duration | 1m |
| `connect_timeout` | Connection timeout duration | 1m |
| `server_selection_timeout` | Server selection timeout | - |
| `username_template` | Template for generating usernames | See below |

### Default username template

```
{{ printf "v-%s-%s-%s-%s" (.DisplayName | truncate 15) (.RoleName | truncate 15) (random 20) (unix_time) | replace "." "-" | truncate 100 }}
```

## Creating roles

```bash
bao write database/roles/my-role \
    db_name=my-mongodb-database \
    creation_statements='{"db": "admin", "roles": [{"role": "readWrite", "db": "mydb"}]}' \
    default_ttl="1h" \
    max_ttl="24h"
```

### Creation statement format

The creation statement is a JSON object with the following structure:

```json
{
  "db": "admin",
  "roles": [
    {"role": "readWrite", "db": "mydb"},
    {"role": "read", "db": "otherdb"}
  ]
}
```

- `db`: The database where the user will be created (defaults to "admin")
- `roles`: Array of role objects, each containing:
  - `role`: The MongoDB role name
  - `db`: The database the role applies to (optional)

## Generating credentials

```bash
bao read database/creds/my-role
```

Example output:

```
Key                Value
---                -----
lease_id           database/creds/my-role/abcd1234
lease_duration     1h
lease_renewable    true
password           A1B2-C3D4-E5F6-G7H8
username           v-token-my-role-abc123-1234567890
```

## Write concern configuration

The `write_concern` parameter accepts a JSON object or base64-encoded JSON:

```json
{
  "w": 1,
  "wmode": "majority",
  "wtimeout": 5000,
  "j": true
}
```

| Field | Description |
|-------|-------------|
| `w` | Number of nodes to acknowledge the write |
| `wmode` | Write mode (e.g., "majority") |
| `wtimeout` | Timeout in milliseconds |
| `j` | Whether to wait for journal sync |

## TLS Configuration

### Using TLS with CA certificate

```bash
bao write database/config/my-mongodb-database \
    plugin_name=mongodb-database-plugin \
    connection_url="mongodb://{{username}}:{{password}}@localhost:27017/admin?tls=true" \
    username="admin" \
    password="adminpassword" \
    tls_ca=@/path/to/ca.pem
```

### Using X.509 certificate authentication

```bash
bao write database/config/my-mongodb-database \
    plugin_name=mongodb-database-plugin \
    connection_url="mongodb://localhost:27017/admin?tls=true&authMechanism=MONGODB-X509" \
    tls_certificate_key=@/path/to/client-cert-key.pem \
    tls_ca=@/path/to/ca.pem
```

## Development

### Running tests

```bash
make test
```

### Running tests (short mode)

```bash
make test-short
```

### Linting

```bash
make lint
```

### Formatting

```bash
make fmt
```

## License

This project is licensed under the Mozilla Public License 2.0 - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

This plugin is adapted from the [HashiCorp Vault MongoDB database plugin](https://github.com/hashicorp/vault/tree/main/plugins/database/mongodb) for use with OpenBao.
