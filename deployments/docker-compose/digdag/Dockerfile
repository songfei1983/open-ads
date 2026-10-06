FROM eclipse-temurin:25-jre@sha256:fcd7fd7b387f94bb2ac461478a7436ad8e349924c374ea8313919624dceae636

WORKDIR /digdag

RUN apt-get update && \
    apt-get install -y \
    curl \
    wget \
    gnupg \
    apt-transport-https \
    lsb-release \
    && rm -rf /var/lib/apt/lists/*

RUN wget --quiet --no-check-certificate --no-cookies \
    "https://dl.digdag.io/digdag-latest" \
    -O /usr/local/bin/digdag && \
    chmod +x /usr/local/bin/digdag

RUN digdag --version

EXPOSE 65432

ENTRYPOINT ["java", "-jar", "/usr/local/bin/digdag"]
