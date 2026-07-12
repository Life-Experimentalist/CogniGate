package com.cognigate.plugin;

public interface AiProviderHandler {
    String handleRequest(String prompt, String apiKey) throws Exception;
}
