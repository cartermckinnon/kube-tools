#!/usr/bin/env bash

cd $(dirname "$0")

LATEST_CI_VERSION=$(curl --silent "https://storage.googleapis.com/k8s-release-dev/ci/k8s-master.txt")

sed s/latest/ci\\/$LATEST_CI_VERSION/ kind-cluster.yaml | kind create cluster --config=- --name kind-bleeding-edge