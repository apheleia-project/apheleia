package io.apheleia;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Base64;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Locale;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.zip.GZIPOutputStream;

import jakarta.inject.Inject;

import org.apache.commons.compress.archivers.tar.TarArchiveOutputStream;

import com.redhat.hacbs.resources.model.v1alpha1.DependencyBuild;

import io.apheleia.jvmbuildservice.model.JBSConfig;
import io.apheleia.jvmbuildservice.model.RebuiltArtifact;
import io.apheleia.model.ComponentBuild;
import io.fabric8.kubernetes.client.KubernetesClient;
import io.quarkus.logging.Log;
import picocli.CommandLine;

@CommandLine.Command(name = "download-sources")
public class DownloadSources implements Runnable {

    @CommandLine.Parameters(description = "The name of the ComponentBuild object to download the sources for.", index = "0")
    String name;

    @CommandLine.Parameters(description = "The path to the resulting tar.gz file", index = "1")
    Path path;

    @Inject
    KubernetesClient client;

    @Override
    public void run() {
        JBSConfig config = client.resources(JBSConfig.class).withName("jvm-build-config").get();

        String value = client.secrets().withName("jvm-build-image-secrets").get().getData().get(".dockerconfigjson");
        OCIRegistryRepositoryClient registryRepositoryClient = new OCIRegistryRepositoryClient(
                Optional.ofNullable(config.getSpec().getHost()).orElse("quay.io"),
                config.getSpec().getOwner(),
                Optional.ofNullable(config.getSpec().getRepository()).orElse("artifact-deployments"),
                Optional.ofNullable(new String(Base64.getDecoder().decode(value), StandardCharsets.UTF_8)), false);
        var build = client.resources(ComponentBuild.class).withName(name).get();
        var artifacts = client.resources(RebuiltArtifact.class).list().getItems();
        Map<String, RebuiltArtifact> gavToArtifact = new HashMap<>();
        Map<String, String> imageToDependencyBuild = new HashMap<>();

        for (var i : artifacts) {
            gavToArtifact.put(i.getSpec().getGav(), i);
            for (var o : i.getMetadata().getOwnerReferences()) {
                if (o.getKind().toLowerCase(Locale.ROOT).startsWith("dependencybuild")) {
                    imageToDependencyBuild.put(i.getSpec().getImage(), o.getName());
                }
            }
        }
        Set<String> images = new HashSet<>();
        for (var i : build.getSpec().getArtifacts()) {
            RebuiltArtifact rebuiltArtifact = gavToArtifact.get(i);
            if (rebuiltArtifact == null) {
                Log.errorf("Unable to find rebuilt artifact for %s", i);
            } else {
                images.add(rebuiltArtifact.getSpec().getImage());
            }
        }
        try (TarArchiveOutputStream out = new TarArchiveOutputStream(new GZIPOutputStream(Files.newOutputStream(path)))) {
            for (var image : images) {
                var dbname = imageToDependencyBuild.get(image);
                DependencyBuild db = client.resources(DependencyBuild.class).withName(dbname).get();
                String path = db.getSpec().getScm().getScmURL().replace("https://", "").replace("/", "-") + "-"
                        + db.getSpec().getScm().getTag().replace("/", "-") + ".tar";
                System.out.println(path);
                try {
                    Path extracted = registryRepositoryClient.extractImage(image.substring(image.lastIndexOf(":") + 1)).get();
                    Path target = extracted.resolve("layer-0.tar");
                    out.putArchiveEntry(out.createArchiveEntry(target, path));
                    Files.copy(target, out);
                    out.closeArchiveEntry();
                } catch (IOException e) {
                    throw new RuntimeException(e);
                }
            }
        } catch (IOException e) {
            throw new RuntimeException(e);
        }

    }
}
