# Cloudland Control Plane Gateway Initialization Scripts

This directory contains scripts for initializing and managing the Cloudland Control Plane Gateway system.

## init_system.sh

The `init_system.sh` script is used to initialize the Cloudland system by registering regions and seeding administrator accounts based on a JSON configuration file.

### Prerequisites

- `curl`: Used for making API requests.
- `python3`: Used for parsing the JSON configuration file.
- Access to the Cloudland API.

### Usage

Run the script from the project root:

```bash
bash scripts/init_system.sh
```

### Configuration

The script can be configured using environment variables. If not set, it uses default values.

| Environment Variable | Description | Default Value |
|----------------------|-------------|---------------|
| `BASE_URL` | The base URL of the Cloudland API. | `http://localhost:8000/api/v1` |
| `CONFIG_FILE` | Path to the regions configuration JSON file. | `scripts/regions_config.json` |
| `ROOT_USER` | The root username for initial authentication. | `root` |
| `ROOT_PASS` | The root password for initial authentication. | `root_password_change_me` |

Example of running with custom configuration:

```bash
BASE_URL="http://api.example.com/api/v1" ROOT_PASS="secure_password" bash scripts/init_system.sh
```

### Configuration File Format (`regions_config.json`)

The configuration file should be a JSON array of region objects. Each object must contain the following fields:

- `name`: Unique name of the region.
- `endpoint_url`: API endpoint for the region.
- `description`: A brief description of the region.
- `admin_username`: Username for the region's administrator account.
- `admin_password`: Password for the region's administrator account.
- `admin_email`: (Optional) Email address for the region's administrator account.

Example:

```json
[
  {
    "name": "tor-04",
    "endpoint_url": "http://tor-04.cloudland.local",
    "description": "Toronto Data Center 04",
    "admin_username": "admin",
    "admin_password": "region_password",
    "admin_email": "admin@example.com"
  }
]
```

## Other Scripts

- `analyze_swagger.py`: Analyzes Swagger/OpenAPI documentation.
- `codegen_swagger.py`: Generates code based on Swagger/OpenAPI documentation.
- `get_token.py`: Simple utility to obtain an authentication token.
