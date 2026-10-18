Code for the blog post: https://blog.rasc.ch/2026/04/spring-boot-startup.html

Run the Java 27 suite and the Java 28 experiments with `task bench:all`. Docker must run Linux
containers; Go and Task run on the host. Remote Linux execution is described in
[run/README.md](run/README.md).

Java 27 uses the official `azul-zulu:27-jdk` builder and `azul-zulu:27-jre`
runtime images. CRaC uses a separate Zulu 27.0.0 runtime built from Azul's
checksum-verified archives by `task build:crac-runtime`, which runs automatically
before `build:crac`. The archive versions and AMD64/ARM64 checksums are pinned in
[docker/zulu-crac.Dockerfile](docker/zulu-crac.Dockerfile).

The Java 28 experiment uses the official OpenJDK **28-ea+20** build from
[jdk.java.net](https://jdk.java.net/28/), verified against pinned SHA-256 checksums.
Java 28 is scheduled for **March 2027**. Petclinic is pinned to its September 29, 2026
revision ([`500158f732419217507c7656904b8e6aa1bcc0d6`](https://github.com/spring-projects/spring-petclinic/commit/500158f732419217507c7656904b8e6aa1bcc0d6)) and
compiled with the Zulu Java 27 builder and Spring Boot 4.1.1, so changing the
runtime does not also change the application bytecode. Training always uses the
same JDK, GC, heap limit, and CPU as the measured runtime. Build the caches on the
benchmark host, since Java 28 cached native code depends on CPU features.

| Result variant | Runtime / experiment |
| --- | --- |
| `aot-cache-java28` | G1, native code cached by JEP 544 |
| `aot-cache-java28-no-aot-code` | Same image/cache, native-code loading disabled |
| `spring-aot-aot-cache-java28` | Spring AOT plus JEP 544, G1 |

The `no-aot-code` control uses `-XX:+UnlockDiagnosticVMOptions
-XX:-AOTCodeCaching`. Cached Java 28 runs use `-XX:AOTMode=required` to reject an
incompatible cache instead of silently falling back. See [JEP 544](https://openjdk.org/jeps/544).

All Java 28 runs use G1 and `-Xmx1g`. The benchmark stops at the first HTTP 200
and does not measure warmup or sustained-load tail latency.

```sh
task java28:bench                         # build, 15 starts per variant, report
task java28:measure BENCH_RUNS=3           # rerun existing images
task java28:report
```

Results are written to `results/java28/`, separately from the Java 27
results. JSON records include the Java version, Docker host, image ID, source
labels, runtime arguments, and each measured start. The report computes the
regular AOT-cache native-code on/off delta.

To test a newer EA build, override `JAVA28_BUILD`, `JAVA28_SHA256_AMD64`, and
`JAVA28_SHA256_ARM64` with values from the download page. Set
`JAVA28_RESULTS_DIR=results/java28-ea-N` to preserve the previous measurements.
`JAVA28_PETCLINIC_REF` and `JAVA28_MAX_HEAP` are also configurable. Keep the
application revision, hardware, and configuration fixed when comparing builds.
