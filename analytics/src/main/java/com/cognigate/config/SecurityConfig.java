package com.cognigate.config;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.config.annotation.web.builders.HttpSecurity;
import org.springframework.security.config.annotation.web.configuration.EnableWebSecurity;
import org.springframework.security.config.annotation.web.configurers.AbstractHttpConfigurer;
import org.springframework.security.config.http.SessionCreationPolicy;
import org.springframework.security.web.SecurityFilterChain;
import org.springframework.security.web.authentication.UsernamePasswordAuthenticationFilter;

@Configuration
@EnableWebSecurity
public class SecurityConfig {

    /**
     * The same floor the gateway puts under its own bootstrap credential. It is
     * well below what {@code openssl rand -hex 32} produces; the point is not to
     * measure entropy but to refuse the placeholder that ships in
     * {@code .env.example}, which is otherwise a perfectly valid non-blank
     * string published in the repository.
     */
    private static final int MIN_LENGTH = 16;

    private final String token;

    /**
     * There is deliberately no default token, for the same reason there is no
     * default master key: a built-in fallback would be public knowledge, and
     * every deployment that forgot to set the variable would be publishing an
     * open metering and administration API on a host port. Absent configuration
     * fails fast at startup instead.
     */
    public SecurityConfig(@Value("${ANALYTICS_TOKEN:}") String token) {
        if (token == null || token.strip().length() < MIN_LENGTH) {
            throw new IllegalArgumentException(
                "ANALYTICS_TOKEN is not set, or is shorter than " + MIN_LENGTH + " characters. Without a "
                    + "real one /api/** would accept usage records and tenant administration from anyone "
                    + "who can reach this port. It must be the same value the gateway is given as "
                    + "ANALYTICS_TOKEN. Generate one with: openssl rand -hex 32");
        }
        this.token = token;
    }

    @Bean
    public SecurityFilterChain securityFilterChain(HttpSecurity http) throws Exception {
        http
            // Nothing here is reached from a browser session, so there is no
            // cookie for a cross-site request to ride on.
            .csrf(AbstractHttpConfigurer::disable)
            // CORS is off rather than permissive. The only clients are the
            // gateway and an operator's own tooling, both of which send the
            // token from a server; a policy that allowed every origin to send
            // an Authorization header would have made any page on the internet
            // a caller the moment it learned the token.
            .cors(AbstractHttpConfigurer::disable)
            .sessionManagement(session -> session.sessionCreationPolicy(SessionCreationPolicy.STATELESS))
            .addFilterBefore(new ApiTokenFilter(token), UsernamePasswordAuthenticationFilter.class)
            .authorizeHttpRequests(auth -> auth
                // The container's HEALTHCHECK has no credential to present, and
                // a health endpoint that refused it could never tell
                // `compose up --wait` that the service is serving. Safe to
                // leave open because management.endpoint.health.show-details is
                // never: it answers UP or DOWN and nothing else.
                .requestMatchers("/actuator/health", "/actuator/health/**").permitAll()
                // Already gated: anything that reaches here got past the filter
                // above, which means it presented the token.
                .requestMatchers("/api/**").permitAll()
                // Everything else — the rest of actuator included — has no
                // caller, so it has no reason to answer.
                .anyRequest().denyAll()
            );
        return http.build();
    }
}
