package com.cognigate.entity;

import jakarta.persistence.*;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.AllArgsConstructor;
import java.time.LocalDateTime;

@Entity
@Table(name = "usage_metric")
@Data
@NoArgsConstructor
@AllArgsConstructor
public class UsageMetric {

    @Id
    @GeneratedValue(strategy = GenerationType.IDENTITY)
    private Long id;

    @Column(name = "prompt_tokens", nullable = false)
    private Integer promptTokens;

    @Column(name = "completion_tokens", nullable = false)
    private Integer completionTokens;

    @Column(name = "total_tokens", nullable = false)
    private Integer totalTokens;

    @Column(name = "recorded_at", nullable = false)
    private LocalDateTime recordedAt;

    @ManyToOne(fetch = FetchType.LAZY)
    @JoinColumn(name = "tenant_id", nullable = false)
    private Tenant tenant;
}
