#!/usr/bin/env bash
set -e

if [ ! -d "/opt/oauth2playground" ]; then
  git clone https://github.com/francescobianco/oauth2playground /opt/oauth2playground
fi

cd /opt/oauth2playground || exit 1

git pull --no-rebase

[ ! -f ".env" ] && cp -f .env.example .env
cp -f compose.override.example compose.override.yml

docker compose up -d --build --force-recreate
