# Progressive Overload Fitness Tracker

A backend API for weightlifters to track workouts and visualize progressive overload.

## Quick Start

### Local Development (In-Memory)

Run without Docker using in-memory repositories:

```bash
go run cmd/api/main.go
```

The API will start on `http://localhost:8080` with in-memory storage (data is lost on restart).

### Local Development (Docker + SQLite)

Run with Docker and persistent SQLite storage:

```bash
# Build and start
docker-compose up -d

# View logs
docker-compose logs -f api

# Stop
docker-compose down
```

The API runs on `http://localhost:8080` with data persisted to `./data/app.db`.

## API Endpoints

### Health Check
```bash
curl http://localhost:8080/health
```

### List Exercises
```bash
curl http://localhost:8080/exercises
```

### Get Exercise by ID
```bash
curl http://localhost:8080/exercises/barbell-high-bar-back-squat
```

### Log a Workout

**Note**: DEV-ONLY - requires `X-User-ID` header until OAuth is implemented.

```bash
curl -X POST http://localhost:8080/workouts \
  -H "Content-Type: application/json" \
  -H "X-User-ID: your-user-id" \
  -d '{
    "name": "Leg Day",
    "performed_at": "2026-05-05T14:00:00Z",
    "notes": "Felt strong today",
    "exercises": [
      {
        "exercise_id": "barbell-high-bar-back-squat",
        "notes": "Good depth",
        "sets": [
          {"reps": 5, "weight": 135, "unit": "lb"},
          {"reps": 5, "weight": 185, "unit": "lb"},
          {"reps": 5, "weight": 225, "unit": "lb"}
        ]
      }
    ]
  }'
```

## Configuration

Environment variables:

- `DATABASE_URL` - Path to SQLite database file (default: in-memory if not set)
- `SERVER_ADDR` - HTTP server address (default: `:8080`)

## Project Structure

See [CLAUDE.md](./CLAUDE.md) for detailed architecture and development guidelines.

## Deployment

The Docker setup mirrors production deployment. For EC2:

1. Copy `docker-compose.yml` to your EC2 instance
2. Set up volume mount for `/data` (database persistence)
3. Run `docker-compose up -d`
4. Configure reverse proxy (nginx/caddy) for HTTPS
5. (Optional) Add Litestream sidecar for S3 backups

## Development

```bash
# Build
go build ./...

# Run locally without Docker
go run cmd/api/main.go

# Run with SQLite locally
DATABASE_URL=./data/app.db go run cmd/api/main.go

# Build Docker image
docker build -t fitness-api .
```
