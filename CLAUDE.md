# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`fast_ship` is a full-stack web application for managing GitHub issues, versions, and artifact shipping:

- **Backend**: Go + Gin framework with SQLite database
- **Frontend**: React + Vite with TypeScript
- **Architecture**: Clean separation with API-first design

## Development Commands

### Root Level Commands (Primary)

Use these commands from the repository root for unified development workflow:

```bash
# Development (recommended)
make dev                  # Start both backend (port 4888) and frontend (port 4999)
make dev-server           # Start only backend
make dev-web              # Start only frontend

# Building
make build                # Build both backend binary and frontend artifacts
make build-server         # Build Go binary to server/bin/fast_ship
make build-web            # Build React app to web/dist

# Testing
make test                 # Run all tests
make test-server          # Run Go tests with: cd server && go test ./...
make test-web             # Run frontend tests with: pnpm --dir web test

# Quality Checks
make lint                 # Run all linting
make lint-server          # Check Go formatting and run go vet
make lint-web             # Run ESLint + TypeScript checks

# Maintenance
make tidy                 # Tidy Go dependencies
make clean                # Clean build artifacts
```

### Backend-Specific Commands

```bash
cd server
make dev                  # Run with hot reload (go run ./cmd/server)
make build                # Build binary to bin/fast_ship
make run                  # Build and run production binary
make test                 # Run Go tests
make tidy                 # Tidy dependencies
make clean                # Clean bin/ and data/
```

### Frontend-Specific Commands

```bash
cd web
pnpm dev                 # Start dev server on port 4999
pnpm build               # Build production bundle (runs typecheck first)
pnpm typecheck           # Run TypeScript compiler check
pnpm lint                # Run ESLint
pnpm check               # Run both lint and typecheck
pnpm test                # Run Vitest tests
pnpm preview             # Preview production build
```

## Architecture

### Backend Structure (`server/`)

Clean architecture with clear separation of concerns:

```
server/
├── cmd/server/          # Application entry point
├── internal/
│   ├── handler/         # HTTP request handlers (controllers)
│   ├── service/         # Business logic layer
│   ├── repository/      # Data access layer
│   ├── model/          # Database models
│   ├── middleware/     # HTTP middleware (auth, CORS, etc.)
│   ├── router/         # Route setup and configuration
│   ├── config/         # Configuration loading
│   └── pkg/            # Internal packages (GitHub media proxy, storage)
├── configs/            # Configuration files (config.yaml)
├── migrations/         # Database migrations
└── data/              # Runtime data (SQLite DB, uploads)
```

**Key Patterns**:
- **Handler Layer**: Receives HTTP requests, calls services, returns responses
- **Service Layer**: Contains business logic, calls multiple repositories
- **Repository Layer**: Direct database operations via GORM
- **Middleware**: Authentication (JWT + API Key), CORS, logging
- **Router**: Groups routes by authentication requirements (JWT-only, API Key, public)

**Authentication**:
- JWT tokens for user operations
- API Keys for programmatic access
- Route-level middleware enforces auth requirements
- Some endpoints support both JWT and API Key authentication

### Frontend Structure (`web/`)

Modern React with file-based routing and component-driven architecture:

```
web/
├── src/
│   ├── routes/         # File-based routing with nested layouts
│   │   ├── _layout.tsx           # Main authenticated layout
│   │   ├── _auth-layout.tsx      # Authenticated pages layout
│   │   ├── settings/             # Settings section with nested routes
│   │   ├── projects/             # Project management pages
│   │   ├── issues/               # Issue pages
│   │   └── __tests__/           # Route integration tests
│   ├── components/     # Reusable UI components
│   │   ├── ui/        # shadcn/ui components
│   │   ├── issues/    # Issue-specific components
│   │   ├── projects/  # Project-specific components
│   │   └── layout/    # Layout components
│   ├── lib/           # Utilities, API clients, state management
│   ├── types/         # TypeScript type definitions
│   └── test/          # Test utilities and setup
├── public/            # Static assets
└── dist/             # Build output (generated)
```

**Key Technologies**:
- **Routing**: React Router v7 with file-based lazy loading
- **State Management**: TanStack Query for server state, Zustand for client state
- **UI Components**: shadcn/ui (Radix UI primitives + Tailwind CSS)
- **Forms**: React Hook Form + Zod validation
- **HTTP Client**: ky (fetch wrapper)
- **Testing**: Vitest + Testing Library + happy-dom
- **Theme**: next-themes for light/dark/system theme switching

**Routing Pattern**:
- Lazy-loaded route components for code splitting
- Nested layouts (`<Route>` with `element` containing `<Outlet />`)
- Protected routes require authentication (redirect to login)
- Settings pages use nested routing with sidebar navigation

## Configuration

### Backend Configuration

Location: `server/configs/config.yaml`

Key settings:
- Server port (default: 4888)
- SQLite database path: `./data/fast_ship.db`
- JWT secret and expiration
- Upload file size limits (default: 500MB)
- Issue auto-sync configuration

Override with environment variable:
```bash
CONFIG_PATH=/custom/config.yaml make dev-server
```

### Frontend Configuration

Location: `web/vite.config.ts`

Key settings:
- Dev server port: 4999
- API proxy: `/api` → `http://localhost:4888`
- Path alias: `@` → `./src`
- Test environment: happy-dom

## Development Workflow

### Starting Development

1. **Install dependencies** (first time only):
   ```bash
   cd web && pnpm install
   ```

2. **Start development servers**:
   ```bash
   make dev
   ```
   This starts both backend (http://localhost:4888) and frontend (http://localhost:4999).

3. **Make changes**:
   - Backend changes auto-reload via `go run`
   - Frontend changes hot-reload via Vite HMR

### Running Tests

```bash
# All tests
make test

# Backend only
cd server && go test ./...

# Frontend only
cd web && pnpm test
```

Frontend tests use Vitest with happy-dom. Test files are co-located with components using `__tests__` directories.

### Quality Checks

```bash
# Full check
make lint

# Backend only
cd server && gofmt -l . && go vet ./...

# Frontend only
cd web && pnpm check  # Runs both ESLint and TypeScript checks
```

## Key Concepts

### Issue Synchronization

The system automatically syncs GitHub issues based on configuration:
- Auto-sync on startup (configurable)
- Periodic sync every 15 minutes (configurable)
- Manual sync via API endpoint

### Version Shipping

A "version" represents a release with:
- Associated issues
- Artifacts (install packages)
- Shipping status workflow
- Pre-ship validation checks

### Asset Management

Two types of assets:
1. **Issue Assets**: Images/files attached to issues
2. **Draft Assets**: Temporary uploads during issue creation

Assets are stored in `server/data/uploads/` and served via API endpoints.

### GitHub Integration

- GitHub media proxy for caching images
- Issue comments and timeline sync
- Repository labels fetching
- Issue filtering by labels and milestones

## Docker Deployment

The repository includes full Docker support:

- **Dockerfile**: Multi-stage build (frontend → static files → Go binary)
- **GitHub Actions**: Auto-build on tag push (`v1.0.0` format)
- **Image Registry**: `ghcr.io/<owner>/<repo>`
- **Tags**: Both version tags and `latest`

```bash
# Local Docker
docker-compose build
docker-compose up -d

# Production deployment
git tag v1.0.0
git push origin v1.0.0  # Triggers GitHub Actions build
```

## Common Patterns

### Adding a New API Endpoint

1. **Backend**: Create handler → service → repository methods
2. **Router**: Add route with appropriate auth middleware
3. **Frontend**: Add API client function in `src/lib/`
4. **Frontend**: Create React Query hooks for data fetching

### Adding a New Page

1. **Frontend**: Create route file in `src/routes/`
2. **Frontend**: Add route in `src/App.tsx`
3. **Frontend**: Add navigation links if needed
4. **Frontend**: Write tests in `src/routes/__tests__/`

### Database Migrations

Currently using GORM auto-migration. Migrations directory exists but manual migrations not yet implemented.

## Testing Strategy

### Backend Tests

- Unit tests for services and repositories
- Integration tests for handlers
- Table-driven tests for multiple scenarios
- Mock external dependencies (GitHub API, etc.)

### Frontend Tests

- Component tests with Testing Library
- Route integration tests
- User interaction testing
- Happy-dom for Jest-like environment
- 100% test coverage goal for new features

Current test status: 66 tests passing across 11 test files (see `web/TEST_REPORT.md`)

## File Upload Handling

- Max size: 500MB (configurable)
- Storage: `server/data/uploads/`
- Draft assets: Temporary uploads during issue creation
- Issue assets: Permanent attachments to issues
- Both supported in frontend with chunked upload for large files
