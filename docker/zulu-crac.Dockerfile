# Azul publishes Java 27 CRaC archives separately from the standard Docker images.
FROM debian:13-slim
ARG TARGETARCH
ARG ZULU_CRAC_VERSION=27.28.101
ARG ZULU_CRAC_JAVA_VERSION=27.0.0
# Checksums from the Azul package metadata API for these exact archives.
ARG ZULU_CRAC_SHA256_AMD64=3b60413d732ccbf0b2ede6ec29e1bc51e45e4208aa841c73c4da65915aa302f0
ARG ZULU_CRAC_SHA256_ARM64=7e7afc665d789c1bd633fcb017e315c98bcb5d5894e91e83b95978d09ec9fa12
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
RUN case "${TARGETARCH}" in \
      amd64) arch=x64; checksum="${ZULU_CRAC_SHA256_AMD64}" ;; \
      arm64) arch=aarch64; checksum="${ZULU_CRAC_SHA256_ARM64}" ;; \
      *) echo "Unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && curl -fsSL "https://cdn.azul.com/zulu/bin/zulu${ZULU_CRAC_VERSION}-ca-crac-jre${ZULU_CRAC_JAVA_VERSION}-linux_${arch}.tar.gz" -o /tmp/jre.tar.gz \
    && echo "${checksum}  /tmp/jre.tar.gz" | sha256sum -c - \
    && mkdir /opt/java \
    && tar -xzf /tmp/jre.tar.gz --strip-components=1 -C /opt/java \
    && rm /tmp/jre.tar.gz
ENV LANG=C.UTF-8
ENV JAVA_HOME=/opt/java
ENV PATH="/opt/java/bin:${PATH}"
RUN java -version
