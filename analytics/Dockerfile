# CogniGate analytics engine.

# Stage 1: build with Maven on the JDK the project targets.
#
# Pinned to BUILDPLATFORM because the jar is architecture-independent: there is
# nothing to gain from running Maven once per target platform, and doing it
# under emulation is the difference between a two-minute build and a timeout.
FROM --platform=$BUILDPLATFORM maven:3.9-eclipse-temurin-25 AS builder

WORKDIR /app
COPY pom.xml .
RUN mvn dependency:go-offline -B

COPY src ./src
RUN mvn package -DskipTests -B

# Stage 2: runtime.
FROM eclipse-temurin:25-jre-jammy

# curl is here for the healthcheck below and nothing else. The JRE image ships
# without it, and a container that cannot be probed cannot be health-gated.
RUN apt-get update \
    && apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/*

# An unprivileged user: the service writes nothing to disk.
RUN useradd --system --uid 10001 --create-home cognigate
USER cognigate

WORKDIR /app
COPY --from=builder /app/target/analytics-1.0.0-SNAPSHOT.jar app.jar

EXPOSE 8081

# The start period is generous because a Spring context plus the first JPA
# schema check is slow, and a restart loop during startup is worse than waiting.
HEALTHCHECK --interval=5s --timeout=3s --start-period=45s --retries=6 \
  CMD curl -fsS http://127.0.0.1:8081/actuator/health || exit 1

ENTRYPOINT ["java", "-jar", "app.jar"]
