package io.apheleia.jvmbuildservice.model;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;

@JsonIgnoreProperties(ignoreUnknown = true)
public class JBSConfigSpec {
    private String host;
    private String port;
    private String owner;
    private String repository;
    private String insecure;
    private String prependTag;

    public String getHost() {
        return host;
    }

    public JBSConfigSpec setHost(String host) {
        this.host = host;
        return this;
    }

    public String getPort() {
        return port;
    }

    public JBSConfigSpec setPort(String port) {
        this.port = port;
        return this;
    }

    public String getOwner() {
        return owner;
    }

    public JBSConfigSpec setOwner(String owner) {
        this.owner = owner;
        return this;
    }

    public String getRepository() {
        return repository;
    }

    public JBSConfigSpec setRepository(String repository) {
        this.repository = repository;
        return this;
    }

    public String getInsecure() {
        return insecure;
    }

    public JBSConfigSpec setInsecure(String insecure) {
        this.insecure = insecure;
        return this;
    }

    public String getPrependTag() {
        return prependTag;
    }

    public JBSConfigSpec setPrependTag(String prependTag) {
        this.prependTag = prependTag;
        return this;
    }
}
