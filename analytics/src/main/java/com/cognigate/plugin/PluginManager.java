package com.cognigate.plugin;

import org.codehaus.janino.SimpleCompiler;
import org.springframework.stereotype.Service;

import java.io.StringReader;
import java.util.HashMap;
import java.util.Map;

@Service
public class PluginManager {

    private final Map<String, PluginHolder> plugins = new HashMap<>();

    public synchronized void loadPlugin(String className, String sourceCode) throws Exception {
        SimpleCompiler compiler = new SimpleCompiler();
        compiler.setParentClassLoader(this.getClass().getClassLoader());
        compiler.cook(new StringReader(sourceCode));

        ClassLoader classLoader = compiler.getClassLoader();
        Class<?> clazz = classLoader.loadClass(className);

        if (!AiProviderHandler.class.isAssignableFrom(clazz)) {
            throw new IllegalArgumentException("Plugin class " + className + " must implement AiProviderHandler");
        }

        AiProviderHandler handler = (AiProviderHandler) clazz.getDeclaredConstructor().newInstance();

        // Evict previous classloader class to prevent Metaspace leaks
        if (plugins.containsKey(className)) {
            plugins.remove(className);
        }

        plugins.put(className, new PluginHolder(handler, classLoader));
    }

    public synchronized AiProviderHandler getPlugin(String className) {
        PluginHolder holder = plugins.get(className);
        return holder != null ? holder.handler : null;
    }

    private static class PluginHolder {
        final AiProviderHandler handler;
        final ClassLoader classLoader;

        PluginHolder(AiProviderHandler handler, ClassLoader classLoader) {
            this.handler = handler;
            this.classLoader = classLoader;
        }
    }
}
