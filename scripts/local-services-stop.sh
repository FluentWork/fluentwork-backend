#!/usr/bin/env bash
set -euo pipefail

echo "🛑 Stopping local MySQL and Redis services..."

# Stop MySQL
if brew services list | grep -q "mysql.*started"; then
  echo "▶️  Stopping MySQL..."
  brew services stop mysql
  echo "✅ MySQL stopped"
else
  echo "ℹ️  MySQL is not running"
fi

# Stop Redis
if brew services list | grep -q "redis.*started"; then
  echo "▶️  Stopping Redis..."
  brew services stop redis
  echo "✅ Redis stopped"
else
  echo "ℹ️  Redis is not running"
fi

echo ""
echo "✅ All services stopped"
