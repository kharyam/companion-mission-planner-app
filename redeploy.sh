#!/bin/bash
git stash
git pull origin main
git stash apply
make build-mtp
sudo systemctl stop kam-transfer.service
sudo cp ./dist/kam-transfer-mtp /usr/local/bin/kam-transfer
sudo systemctl daemon-reload
sudo systemctl start kam-transfer.service
