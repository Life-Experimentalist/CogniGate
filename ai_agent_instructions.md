# AI Agent Instructions for CogniGate Plugins

You are an AI agent tasked with generating dynamic Java plugins for the CogniGate platform. The CogniGate Domain Engine compiling environment uses **Janino 3.1.12** and **Java 25**. Follow these rules strictly to ensure plugins compile in memory and hot-swap without issues.

---

## 1. Interface Implementation Contract

Every plugin **MUST** implement the `com.cognigate.plugin.AiProviderHandler` interface.

```java
package com.cognigate.plugin;

public interface AiProviderHandler {
    /**
     * Executes the call to the underlying LLM provider.
     *
     * @param prompt The incoming user prompt.
     * @param apiKey The decrypted API credentials for the LLM provider.
     * @return The raw response from the LLM provider.
     * @throws Exception If any communication, parsing, or protocol error occurs.
     */
    String handleRequest(String prompt, String apiKey) throws Exception;
}
```

---

## 2. Compilation Constraints (Janino Limits)

Janino is a lightweight, fast compiler but does NOT support all Java features.
- **No Generics (Type Parameters) in certain complex declarations**: Janino supports basic generics, but keep declarations simple.
- **No Lambda Expressions / Method References prior to Java 8**: Since we compile on Java 25, basic lambda syntax is supported, but standard anonymous inner classes are safer if you hit Janino-specific compiler issues.
- **No Annotations at compile time**: Do not annotate class methods with Spring annotations (like `@Autowired` or `@Component`). The class will be instantiated using `Class.getDeclaredConstructor().newInstance()` and is NOT managed by Spring ApplicationContext out of the box unless injected programmatically.
- **Single Class definition**: The source code uploaded must contain exactly one public class implementing `AiProviderHandler`.

---

## 3. Security, State, and Concurrency Rules

- **Thread-Safety**: The compiled handler instance will be shared globally and called concurrently by virtual thread executors. It **MUST be completely stateless**. Do not use mutable class-level instance variables.
- **Sandboxing**: Avoid trying to access local files, system environment variables, or executing system commands. Only perform HTTP integrations and basic serialization.
- **Resource Cleanup**: If you open HTTP connections or streams, always close them in a `finally` block or use try-with-resources.

---

## 4. Example Reference Implementation

Here is how you should structure your uploaded `.java` file:

```java
package com.cognigate.plugin;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;

public class CustomProviderHandler implements AiProviderHandler {

    @Override
    public String handleRequest(String prompt, String apiKey) throws Exception {
        HttpClient client = HttpClient.newHttpClient();
        
        String jsonPayload = "{\"prompt\": \"" + prompt.replace("\"", "\\\"") + "\"}";

        HttpRequest request = HttpRequest.newBuilder()
                .uri(URI.create("https://api.customprovider.com/v1/chat"))
                .header("Content-Type", "application/json")
                .header("Authorization", "Bearer " + apiKey)
                .POST(HttpRequest.BodyPublishers.ofString(jsonPayload))
                .build();

        HttpResponse<String> response = client.send(request, HttpResponse.BodyHandlers.ofString());

        if (response.statusCode() != 200) {
            throw new RuntimeException("Provider returned error status: " + response.statusCode() + " - " + response.body());
        }

        return response.body();
    }
}
```
