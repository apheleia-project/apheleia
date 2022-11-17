package io.apheleia;

import io.apheleia.kube.RebuiltArtifact;
import io.apheleia.kube.UserConfig;
import io.fabric8.kubernetes.api.model.KubernetesResourceList;
import io.fabric8.kubernetes.client.KubernetesClient;
import io.fabric8.kubernetes.client.dsl.MixedOperation;
import io.fabric8.kubernetes.client.dsl.Resource;
import io.quarkus.picocli.runtime.annotations.TopCommand;
import org.apache.maven.repository.internal.MavenRepositorySystemUtils;
import org.eclipse.aether.DefaultRepositorySystemSession;
import org.eclipse.aether.RepositorySystem;
import org.eclipse.aether.artifact.Artifact;
import org.eclipse.aether.artifact.DefaultArtifact;
import org.eclipse.aether.connector.basic.BasicRepositoryConnectorFactory;
import org.eclipse.aether.deployment.DeployRequest;
import org.eclipse.aether.deployment.DeploymentException;
import org.eclipse.aether.impl.DefaultServiceLocator;
import org.eclipse.aether.repository.LocalRepository;
import org.eclipse.aether.repository.RemoteRepository;
import org.eclipse.aether.spi.connector.RepositoryConnectorFactory;
import org.eclipse.aether.spi.connector.transport.TransporterFactory;
import org.eclipse.aether.transport.file.FileTransporterFactory;
import org.eclipse.aether.transport.http.HttpTransporterFactory;
import org.eclipse.aether.util.repository.AuthenticationBuilder;
import picocli.CommandLine;

import javax.inject.Inject;
import java.io.IOException;
import java.nio.file.FileVisitResult;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.SimpleFileVisitor;
import java.nio.file.attribute.BasicFileAttributes;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.function.UnaryOperator;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import java.util.stream.Collectors;

@TopCommand
@CommandLine.Command
public class DeployCommand implements Runnable {

    static final int CURRENT_VERSION = 1;
    public static final String APHELIA_DEPLOYED = "aphelia-deployed";

    @Inject
    KubernetesClient client;

    public void run() {
        try {
            UserConfig config = client.resources(UserConfig.class).withName("jvm-build-config").get();
            OCIRegistryRepositoryClient registryRepositoryClient = new OCIRegistryRepositoryClient(
                    Optional.ofNullable(config.getSpec().getHost()).orElse("quay.io"),
                    config.getSpec().getOwner(),
                    Optional.ofNullable(config.getSpec().getRepository()).orElse("artifact-deployments"),
                    Optional.ofNullable(System.getenv("QUAY_TOKEN")),
                    Optional.ofNullable(config.getSpec().getPrependTag()), false);

            RepositorySystem system = newRepositorySystem();
            DefaultRepositorySystemSession session = MavenRepositorySystemUtils.newSession();
            LocalRepository localRepo = new LocalRepository(Files.createTempDirectory("apheleia").toFile());
            session.setLocalRepositoryManager(system.newLocalRepositoryManager(session, localRepo));

            RemoteRepository distRepo = new RemoteRepository.Builder("repo",
                    "default",
                    "https://rhosak-237843776254.d.codeartifact.us-east-2.amazonaws.com/maven/sdouglas-scratch/")
                    .setAuthentication(new AuthenticationBuilder().addUsername("aws").addPassword(System.getenv("CODEARTIFACT_AUTH_TOKEN")).build()).build();

            MixedOperation<RebuiltArtifact, KubernetesResourceList<RebuiltArtifact>, Resource<RebuiltArtifact>> rebuildArtifacts = client.resources(RebuiltArtifact.class);
            for (var i : rebuildArtifacts.list().getItems()) {
                if (i.getMetadata().getAnnotations() != null) {
                    String version = i.getMetadata().getAnnotations().get(APHELIA_DEPLOYED);
                    if (version == null || Integer.parseInt(version) >= CURRENT_VERSION) {
                        System.out.println("Skipping " + i.getSpec().getGav());
                        continue;
                    }
                }
                String image = i.getSpec().getImage();
                Optional<Path> result = registryRepositoryClient.extractImage(image.substring(image.lastIndexOf(":") + 1));
                if (result.isPresent()) {
                    try {
                        Files.walkFileTree(result.get(), new SimpleFileVisitor<>() {

                            @Override
                            public FileVisitResult preVisitDirectory(Path dir, BasicFileAttributes attrs) throws IOException {
                                try (var stream = Files.list(dir)) {
                                    List<Path> files = stream.collect(Collectors.toList());
                                    for (var i : files) {
                                        System.out.println(i);
                                    }
                                    System.out.println("-------------");
                                    boolean hasPom = files.stream().anyMatch(s -> s.toString().endsWith(".pom"));
                                    if (hasPom) {
                                        Path relative = result.get().relativize(dir);
                                        String group = relative.getParent().getParent().toString().replace("/", ".");
                                        String artifact = relative.getParent().getFileName().toString();
                                        String version = dir.getFileName().toString();
                                        System.out.println("GROUP: " + group + " , ART:" + artifact + " , VERSION: " + version);
                                        Pattern p = Pattern.compile(artifact + "-" + version + "(-(\\w+))?\\.(\\w+)");
                                        DeployRequest deployRequest = new DeployRequest();
                                        deployRequest.setRepository(distRepo);
                                        for (var i : files) {
                                            Matcher matcher = p.matcher(i.getFileName().toString());
                                            if (matcher.matches()) {
                                                System.out.println(i.getFileName());
                                                System.out.println(matcher.group(2));
                                                System.out.println(matcher.group(3));
                                                Artifact jarArtifact = new DefaultArtifact(group, artifact, matcher.group(2), matcher.group(3),
                                                        version);
                                                jarArtifact = jarArtifact.setFile(i.toFile());
                                                deployRequest.addArtifact(jarArtifact);
                                            }
                                        }

                                        try {
                                            system.deploy(session, deployRequest);
                                        } catch (DeploymentException e) {
                                            throw new RuntimeException(e);
                                        }
                                    }


                                    return FileVisitResult.CONTINUE;
                                }
                            }

                        });
                    } catch (IOException e) {
                        throw new RuntimeException(e);
                    }
                } else {
                    System.err.println("Failed to download " + i);
                }
                rebuildArtifacts.withName(i.getMetadata().getName()).edit(new UnaryOperator<RebuiltArtifact>() {
                    @Override
                    public RebuiltArtifact apply(RebuiltArtifact rebuiltArtifact) {
                        if (i.getMetadata().getAnnotations() == null) {
                            i.getMetadata().setAnnotations(new HashMap<>());
                        }
                        i.getMetadata().getAnnotations().put(APHELIA_DEPLOYED, Integer.toString(CURRENT_VERSION));
                        return rebuiltArtifact;
                    }
                });
            }
        } catch (IOException e) {
            throw new RuntimeException(e);
        }
    }

    public static RepositorySystem newRepositorySystem() {
        DefaultServiceLocator locator = MavenRepositorySystemUtils.newServiceLocator();
        locator.addService(RepositoryConnectorFactory.class, BasicRepositoryConnectorFactory.class);
        locator.addService(TransporterFactory.class, FileTransporterFactory.class);
        locator.addService(TransporterFactory.class, HttpTransporterFactory.class);

        locator.setErrorHandler(new DefaultServiceLocator.ErrorHandler() {
            @Override
            public void serviceCreationFailed(Class<?> type, Class<?> impl, Throwable exception) {
                exception.printStackTrace();
            }
        });

        return locator.getService(RepositorySystem.class);
    }
}
