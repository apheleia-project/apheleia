package io.apheleia.kube;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonInclude.Include;

import io.fabric8.kubernetes.api.model.Namespaced;
import io.fabric8.kubernetes.client.CustomResource;
import io.fabric8.kubernetes.model.annotation.Group;
import io.fabric8.kubernetes.model.annotation.Version;

@Group(ModelConstants.GROUP)
@Version(ModelConstants.VERSION)
@JsonInclude(Include.NON_NULL)
public class UserConfig extends CustomResource<UserConfigSpec, Void>
        implements Namespaced {

    @Override
    protected UserConfigSpec initSpec() {
        return new UserConfigSpec();
    }

}
