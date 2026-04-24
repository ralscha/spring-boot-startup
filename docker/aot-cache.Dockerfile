ARG SOURCE_IMAGE=alpine/git:2.52.0
ARG BUILDER_IMAGE=azul/zulu-openjdk:26-latest
ARG RUNTIME_IMAGE=azul/zulu-openjdk:26-jre-latest
ARG PETCLINIC_REPO=https://github.com/spring-projects/spring-petclinic.git
ARG PETCLINIC_REF=main
ARG SPRING_BOOT_VERSION=4.0.6

FROM ${SOURCE_IMAGE} AS source
ARG PETCLINIC_REPO
ARG PETCLINIC_REF
WORKDIR /source
RUN git clone --depth 1 --branch ${PETCLINIC_REF} ${PETCLINIC_REPO} app

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
RUN chmod +x mvnw && ./mvnw -q -DskipTests package
RUN cp target/spring-petclinic-*.jar /workspace/application.jar
RUN java -Djarmode=tools -jar /workspace/application.jar extract --layers --destination /workspace/extracted
RUN cp -R /workspace/extracted/dependencies/. /workspace/runtime-root/ \
	&& cp -R /workspace/extracted/spring-boot-loader/. /workspace/runtime-root/ \
	&& cp -R /workspace/extracted/snapshot-dependencies/. /workspace/runtime-root/ \
	&& cp -R /workspace/extracted/application/. /workspace/runtime-root/
WORKDIR /workspace/runtime-root
RUN java -XX:AOTCacheOutput=app.aot -Dspring.context.exit=onRefresh -jar application.jar

FROM ${RUNTIME_IMAGE}
WORKDIR /application
COPY --from=build /workspace/extracted/dependencies/ ./
COPY --from=build /workspace/extracted/spring-boot-loader/ ./
COPY --from=build /workspace/extracted/snapshot-dependencies/ ./
COPY --from=build /workspace/extracted/application/ ./
COPY --from=build /workspace/runtime-root/app.aot /application/app.aot
EXPOSE 8080
ENTRYPOINT ["java", "-XX:AOTCache=app.aot", "-jar", "application.jar"]