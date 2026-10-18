# Official OpenJDK EA build, shared by training and production runs.
FROM debian:13-slim
ARG TARGETARCH
ARG JAVA28_BUILD=20
ARG JAVA28_SHA256_AMD64=fd0b94162fede0e2f7a31106a38d0105b23e96521d8c61c0f1a0967e1383b59d
ARG JAVA28_SHA256_ARM64=a680de4b2f2e749f2257cc707fa250de84b5178bbf61f984f9811ce7d90e5481
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
RUN case "${TARGETARCH}" in \
      amd64) arch=x64; checksum="${JAVA28_SHA256_AMD64}" ;; \
      arm64) arch=aarch64; checksum="${JAVA28_SHA256_ARM64}" ;; \
      *) echo "Unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && curl -fsSL "https://download.java.net/java/early_access/jdk28/${JAVA28_BUILD}/GPL/openjdk-28-ea+${JAVA28_BUILD}_linux-${arch}_bin.tar.gz" -o /tmp/jdk.tar.gz \
    && echo "${checksum}  /tmp/jdk.tar.gz" | sha256sum -c - \
    && mkdir /opt/java \
    && tar -xzf /tmp/jdk.tar.gz --strip-components=1 -C /opt/java \
    && rm /tmp/jdk.tar.gz
ENV JAVA_HOME=/opt/java
ENV PATH="/opt/java/bin:${PATH}"
RUN java -version
