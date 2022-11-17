package io.apheleia.kube;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;

@JsonIgnoreProperties(ignoreUnknown = true)
public class UserConfigSpec {
    private String host;
    private String port;
    private String owner;
    private String repository;
    private String insecure;
    private String prependTag;

    public String getHost() {
        return host;
    }

    public UserConfigSpec setHost(String host) {
        this.host = host;
        return this;
    }

    public String getPort() {
        return port;
    }

    public UserConfigSpec setPort(String port) {
        this.port = port;
        return this;
    }

    public String getOwner() {
        return owner;
    }

    public UserConfigSpec setOwner(String owner) {
        this.owner = owner;
        return this;
    }

    public String getRepository() {
        return repository;
    }

    public UserConfigSpec setRepository(String repository) {
        this.repository = repository;
        return this;
    }

    public String getInsecure() {
        return insecure;
    }

    public UserConfigSpec setInsecure(String insecure) {
        this.insecure = insecure;
        return this;
    }

    public String getPrependTag() {
        return prependTag;
    }

    public UserConfigSpec setPrependTag(String prependTag) {
        this.prependTag = prependTag;
        return this;
    }
}
