FROM eclipse-temurin:11-jre@sha256:717e7ce17d8a0eb3ddecef1195a778473972c016097356839114da01e6f145dd

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
