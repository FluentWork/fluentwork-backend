#!/usr/bin/env bash
set -euo pipefail

echo "📊 Local Services Status"
echo "======================="
echo ""

# Check MySQL
if brew services list | grep -q "mysql.*started"; then
  echo "✅ MySQL: Running"
  if mysqladmin ping -h127.0.0.1 --silent 2>/dev/null; then
    mysql -h127.0.0.1 -e "SELECT VERSION();" 2>/dev/null | tail -1 | xargs echo "   Version:"
  else
    echo "   ⚠️  Service started but not responding"
  fi
else
  echo "❌ MySQL: Not running"
fi

echo ""

# Check Redis
if brew services list | grep -q "redis.*started"; then
  echo "✅ Redis: Running"
  if redis-cli ping 2>/dev/null | grep -q "PONG"; then
    redis-cli INFO server 2>/dev/null | grep "redis_version" | cut -d: -f2 | xargs echo "   Version:"
  else
    echo "   ⚠️  Service started but not responding"
  fi
else
  echo "❌ Redis: Not running"
fi

echo ""
echo "======================="
