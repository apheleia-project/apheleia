package io.apheleia.kube;

import java.util.ArrayList;
import java.util.List;

public class ComponentBuildSpec {
    private List<String> artifacts = new ArrayList<>();
    private String scmURL;
    private String tag;

    public List<String> getArtifacts() {
        return artifacts;
    }

    public ComponentBuildSpec setArtifacts(List<String> artifacts) {
        this.artifacts = artifacts;
        return this;
    }

    public String getScmURL() {
        return scmURL;
    }

    public ComponentBuildSpec setScmURL(String scmURL) {
        this.scmURL = scmURL;
        return this;
    }

    public String getTag() {
        return tag;
    }

    public ComponentBuildSpec setTag(String tag) {
        this.tag = tag;
        return this;
    }

}
