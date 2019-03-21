#!/bin/bash

mkdir -p tls

openssl genrsa -out tls/ca.key 4096

openssl req \
    -x509 -new -nodes -sha256 \
    -key tls/ca.key \
    -days 36500 \
    -subj '/O=Uhaha/CN=Certificate Authority' \
    -out tls/ca.crt

openssl genrsa -out tls/uhaha.key 4096

openssl req \
    -new -sha256 \
    -key tls/uhaha.key \
    -subj '/O=Uhaha/CN=Server' | \
    openssl x509 \
        -req -sha256 \
        -CA tls/ca.crt \
        -CAkey tls/ca.key \
        -CAserial tls/ca.txt \
        -CAcreateserial \
        -days 36500 \
        -out tls/uhaha.crt
