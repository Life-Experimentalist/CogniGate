package com.cognigate.plugin;

public class AnthropicHandler implements AiProviderHandler {

    @Override
    public String handleRequest(String prompt, String apiKey) throws Exception {
        return "Anthropic Claude Response (Mocked): Handled prompt '" + prompt + "' with key length " + (apiKey != null ? apiKey.length() : 0);
    }
}
