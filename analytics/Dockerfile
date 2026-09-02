# Stage 1: Build the Java project using Maven and Java 25
FROM maven:3.9-eclipse-temurin-25 AS builder

WORKDIR /app
COPY pom.xml .
RUN mvn dependency:go-offline -B

COPY src ./src
RUN mvn package -DskipTests -B

# Stage 2: Runtime image
FROM eclipse-temurin:25-jre-jammy

WORKDIR /app
COPY --from=builder /app/target/analytics-1.0.0-SNAPSHOT.jar app.jar

EXPOSE 8081

ENTRYPOINT ["java", "-jar", "app.jar"]
