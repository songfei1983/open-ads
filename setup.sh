#!/bin/zsh

if ! command -v pre-commit&> /dev/null
then
    echo "pre-commit could not be found"
	brew install pre-commit
	go install golang.org/x/tools/cmd/goimports@latest
else
    echo "pre-commit could be found"
fi

# install golangci-lint if missing, matching CI version
if ! command -v golangci-lint &> /dev/null
then
    echo "installing golangci-lint"
    curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
        | sh -s -- -b $(go env GOPATH)/bin v1.76.0
else
    echo "golangci-lint already installed"
fi

if ! command -v direnv &> /dev/null
then
    echo "direnv could not be found"
	brew install direnv
	echo 'eval "$(direnv hook zsh)"' >> ~/.zshrc
else
    echo "direnv could be found"
fi
