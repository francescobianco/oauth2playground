#!/usr/bin/env bash
set -e

[ ! -d "/opt/oauth2playground" ] && git clone https://github.com/francescobianco/oauth2playground /opt/oauth2playground

cd /opt/oauth2playground || exit 1

git pull --no-rebase

#chmod 777 data/
[ ! -f ".env" ] && cp -f .env.example .env
cp -f compose.override.example compose.override.yml

docker compose up -d --build --force-recreate
