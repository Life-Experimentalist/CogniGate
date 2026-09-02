package com.cognigate.plugin;

import tools.jackson.databind.ObjectMapper;
import java.util.Map;

public class JsonMapper implements AiProviderHandler {

    private final String baseUrl;
    private final String authHeaderFormat;

    @SuppressWarnings("unchecked")
    public JsonMapper(String jsonConfig) throws Exception {
        ObjectMapper mapper = new ObjectMapper();
        Map<String, String> config = mapper.readValue(jsonConfig, Map.class);
        this.baseUrl = config.get("baseUrl");
        this.authHeaderFormat = config.get("authHeaderFormat");
    }

    @Override
    public String handleRequest(String prompt, String apiKey) throws Exception {
        String formattedAuth = String.format(authHeaderFormat != null ? authHeaderFormat : "Bearer %s", apiKey);
        return "Calling JSON provider at " + baseUrl + " with prompt: [" + prompt + "] and auth: " + formattedAuth;
    }
}
