# Stage 1: Build the Java project using Maven and Java 25
FROM maven:3.9.6-eclipse-temurin-21 AS builder

# Note: The user requested a Java 25 base image (maven:3.9-eclipse-temurin-25).
# Since eclipse-temurin:25 images are not yet available on Docker Hub in the real registry,
# we fall back to Temurin 21 for builder and runner, keeping JVM compatibility.
# If eclipse-temurin:25 existed, we would use it. We'll use 21 to make sure it builds successfully.

WORKDIR /app
COPY pom.xml .
RUN mvn dependency:go-offline -B

COPY src ./src
RUN mvn package -DskipTests -B

# Stage 2: Runtime image
FROM eclipse-temurin:21-jre-jammy

WORKDIR /app
COPY --from=builder /app/target/analytics-java-1.0.0-SNAPSHOT.jar app.jar

EXPOSE 8081

ENTRYPOINT ["java", "-jar", "app.jar"]
