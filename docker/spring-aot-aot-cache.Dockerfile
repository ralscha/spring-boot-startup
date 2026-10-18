ARG SOURCE_IMAGE=alpine/git:2.54.0
ARG BUILDER_IMAGE=azul-zulu:27-jdk
ARG RUNTIME_IMAGE=azul-zulu:27-jre
ARG PETCLINIC_REPO=https://github.com/spring-projects/spring-petclinic.git
ARG PETCLINIC_REF=500158f732419217507c7656904b8e6aa1bcc0d6
ARG SPRING_BOOT_VERSION=4.1.1

FROM ${SOURCE_IMAGE} AS source
ARG PETCLINIC_REPO
ARG PETCLINIC_REF
WORKDIR /source
RUN git init app && cd app && git remote add origin ${PETCLINIC_REPO} \
    && git fetch --depth 1 origin ${PETCLINIC_REF} && git checkout --detach FETCH_HEAD

FROM ${BUILDER_IMAGE} AS build
ARG SPRING_BOOT_VERSION
WORKDIR /workspace/app
COPY --from=source /source/app /workspace/app
RUN awk -v spring_boot_version="${SPRING_BOOT_VERSION}" ' \
	/<parent>/ { in_parent = 1 } \
	in_parent && /<groupId>org.springframework.boot<\/groupId>/ { boot_parent = 1 } \
	in_parent && /<artifactId>spring-boot-starter-parent<\/artifactId>/ { starter_parent = 1 } \
	in_parent && boot_parent && starter_parent && /<version>[^<]+<\/version>/ { sub(/<version>[^<]+<\/version>/, "<version>" spring_boot_version "</version>"); updated = 1 } \
	/<\/parent>/ { in_parent = 0; boot_parent = 0; starter_parent = 0 } \
	{ print } \
	END { if (!updated) exit 1 } \
' pom.xml > pom.xml.tmp && mv pom.xml.tmp pom.xml
RUN --mount=type=cache,target=/root/.m2 chmod +x mvnw && ./mvnw -q -Pnative -Dmaven.test.skip=true package
RUN cp target/spring-petclinic-*.jar /workspace/application.jar
RUN java -Djarmode=tools -jar /workspace/application.jar extract --layers --destination /workspace/extracted

# Train with the exact production JDK and GC, including for Java 28 native code.
FROM ${RUNTIME_IMAGE} AS training
ARG AOT_JAVA_OPTIONS=
ENV JAVA_TOOL_OPTIONS=${AOT_JAVA_OPTIONS}
WORKDIR /application
COPY --from=build /workspace/extracted/dependencies/ ./
COPY --from=build /workspace/extracted/spring-boot-loader/ ./
COPY --from=build /workspace/extracted/snapshot-dependencies/ ./
COPY --from=build /workspace/extracted/application/ ./
RUN java -Dspring.aot.enabled=true -XX:AOTCacheOutput=app.aot -Dspring.context.exit=onRefresh -jar application.jar

FROM ${RUNTIME_IMAGE}
ARG AOT_JAVA_OPTIONS=
ENV JAVA_TOOL_OPTIONS=${AOT_JAVA_OPTIONS}
WORKDIR /application
COPY --from=build /workspace/extracted/dependencies/ ./
COPY --from=build /workspace/extracted/spring-boot-loader/ ./
COPY --from=build /workspace/extracted/snapshot-dependencies/ ./
COPY --from=build /workspace/extracted/application/ ./
COPY --from=training /application/app.aot /application/app.aot
EXPOSE 8080
ENTRYPOINT ["java", "-Dspring.aot.enabled=true", "-XX:AOTCache=app.aot", "-jar", "application.jar"]
