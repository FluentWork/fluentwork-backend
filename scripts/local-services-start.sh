#!/usr/bin/env bash
set -euo pipefail

echo "🚀 Starting local MySQL and Redis services..."

# Start MySQL
if brew services list | grep -q "mysql.*started"; then
  echo "✅ MySQL is already running"
else
  echo "▶️  Starting MySQL..."
  brew services start mysql
  echo "⏳ Waiting for MySQL to be ready..."
  for i in {1..30}; do
    if mysqladmin ping -h127.0.0.1 --silent 2>/dev/null; then
      echo "✅ MySQL is ready"
      break
    fi
    if [ $i -eq 30 ]; then
      echo "❌ MySQL failed to start within 30 seconds"
      exit 1
    fi
    sleep 1
  done
fi

# Start Redis
if brew services list | grep -q "redis.*started"; then
  echo "✅ Redis is already running"
else
  echo "▶️  Starting Redis..."
  brew services start redis
  echo "⏳ Waiting for Redis to be ready..."
  for i in {1..10}; do
    if redis-cli ping 2>/dev/null | grep -q "PONG"; then
      echo "✅ Redis is ready"
      break
    fi
    if [ $i -eq 10 ]; then
      echo "❌ Redis failed to start within 10 seconds"
      exit 1
    fi
    sleep 1
  done
fi

echo ""
echo "✅ All services started successfully!"
echo ""
echo "MySQL: 127.0.0.1:3306"
echo "Redis: 127.0.0.1:6379"
