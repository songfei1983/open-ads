#!/bin/zsh

if ! command -v pre-commit&> /dev/null
then
    echo "pre-commit could not be found"
	brew install pre-commit
	go install golang.org/x/tools/cmd/goimports@latest
else
    echo "pre-commit could be found"
fi

if ! command -v direnv &> /dev/null
then
    echo "direnv could not be found"
	brew install direnv
	echo 'eval "$(direnv hook zsh)"' >> ~/.zshrc
else
    echo "direnv could be found"
fi
