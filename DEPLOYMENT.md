# Production deployment

Repository: https://github.com/yigerende/siwc.git

Directory: /opt/1panel/apps/sub2api-siwc

From sub2api/deploy, use the private .env with SERVER_PORT=6948 and
independent database, Redis, admin, JWT and TOTP credentials:

```bash
docker compose -p sub2api-siwc -f docker-compose.yml -f docker-compose.production.yml build sub2api-siwc
docker compose -p sub2api-siwc -f docker-compose.yml -f docker-compose.production.yml up -d --no-build
```

Containers: sub2api-siwc-app, sub2api-siwc-postgres, sub2api-siwc-redis.
Only application port 6948 is published. PostgreSQL and Redis are reachable
only on this project's network; all three services use dedicated named volumes.
The production override bounds CPU, memory and Docker log retention.

Optional Google OAuth client credentials are supplied by GEMINI_CLI_OAUTH_CLIENT_ID,
GEMINI_CLI_OAUTH_CLIENT_SECRET, ANTIGRAVITY_OAUTH_CLIENT_ID and
ANTIGRAVITY_OAUTH_CLIENT_SECRET in the private .env; credentials are not embedded
in Git. SIWC uses its own OAuth implementation.

Do not run global Docker cleanup or restart other projects during updates.
Keep .env and the three project volumes when recreating these containers.
