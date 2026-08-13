#!/bin/ash

S6_VERSION="v1.18.1.3"
POSTGREST_VERSION="v12.2.8"

#############################

cd /
adduser --no-create-home --home /server -uid 1000 --shell /usr/bin/bash user --disabled-password --comment ""
mkdir /static
mkdir -p /protected
mkdir -p /saml
chown user /saml

install_packages libpq5 ca-certificates xz-utils entr wget

# Install s6-overlay
wget https://github.com/just-containers/s6-overlay/releases/download/$S6_VERSION/s6-overlay-amd64.tar.gz --no-check-certificate -O /tmp/s6-overlay.tar.gz
tar xfz /tmp/s6-overlay.tar.gz -C /
rm /tmp/s6-overlay.tar.gz

# Install Postgrest
wget https://github.com/PostgREST/postgrest/releases/download/$POSTGREST_VERSION/postgrest-$POSTGREST_VERSION-linux-static-x64.tar.xz --no-check-certificate -O /tmp/postgrest.tar.xz
tar -xf /tmp/postgrest.tar.xz -C /tmp
rm /tmp/postgrest.tar.xz
mv /tmp/postgrest /usr/local/bin/postgrest

apt remove -y wget
apt autoremove
