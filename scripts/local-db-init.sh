#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "🔧 Initializing FluentWork database..."
echo ""

# Create database and user
echo "▶️  Creating database and user..."
mysql -h127.0.0.1 -uroot <<EOF
CREATE DATABASE IF NOT EXISTS fluentwork CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'fw'@'localhost' IDENTIFIED BY 'fw';
GRANT ALL PRIVILEGES ON fluentwork.* TO 'fw'@'localhost';
FLUSH PRIVILEGES;
EOF

echo "✅ Database 'fluentwork' created"
echo "✅ User 'fw' created with password 'fw'"
echo ""

# Apply migrations
if [ -d "$ROOT/migrations" ]; then
  echo "▶️  Applying migrations..."
  for file in "$ROOT"/migrations/*.sql; do
    if [ -f "$file" ]; then
      echo "   Applying $(basename "$file")"
      mysql -h127.0.0.1 -ufw -pfw fluentwork < "$file"
    fi
  done
  echo "✅ Migrations applied"
else
  echo "ℹ️  No migrations directory found, skipping..."
fi

echo ""
echo "✅ Database initialization complete!"
echo ""
echo "Connection string:"
echo "  MYSQL_DSN=fw:fw@tcp(127.0.0.1:3306)/fluentwork?parseTime=true&charset=utf8mb4&loc=UTC"
