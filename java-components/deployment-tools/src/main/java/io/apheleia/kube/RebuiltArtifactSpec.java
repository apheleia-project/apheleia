package io.apheleia.kube;

public class RebuiltArtifactSpec {

    private String gav;
    private String image;

    public String getGav() {
        return gav;
    }

    public void setGav(String gav) {
        this.gav = gav;
    }

    public String getImage() {
        return image;
    }

    public RebuiltArtifactSpec setImage(String image) {
        this.image = image;
        return this;
    }
}
